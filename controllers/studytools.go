package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ─── Free/Pro Quota Gate ─────────────────────────────────────────────────────

const (
	FreeFlashcardLimit = 20 // max card/doc cho free user
	FreeQuizDailyLimit = 1  // max quiz generation/ngày cho free user
)

// checkAndIncrementQuota kiểm tra và tăng quota học tập của user (1 quiz/ngày)
// Trả về (allowed, currentCount, error)
func checkStudyQuota(userID, quotaType string) (bool, int, error) {
	ctx := context.Background()

	// Reset quota nếu sang ngày mới
	_, err := config.DB.Exec(ctx, `
		INSERT INTO study_quota (user_id, quiz_gens_today, flashcard_gens_today, last_reset_date)
		VALUES ($1, 0, 0, CURRENT_DATE)
		ON CONFLICT (user_id) DO UPDATE
		SET quiz_gens_today = CASE
			WHEN study_quota.last_reset_date < CURRENT_DATE THEN 0
			ELSE study_quota.quiz_gens_today
		END,
		flashcard_gens_today = CASE
			WHEN study_quota.last_reset_date < CURRENT_DATE THEN 0
			ELSE study_quota.flashcard_gens_today
		END,
		last_reset_date = CURRENT_DATE`, userID)
	if err != nil {
		return false, 0, err
	}

	// Đọc count hiện tại
	var quizCount, flashCount int
	config.DB.QueryRow(ctx,
		`SELECT quiz_gens_today, flashcard_gens_today FROM study_quota WHERE user_id = $1`, userID).
		Scan(&quizCount, &flashCount)

	if quotaType == "quiz" {
		if quizCount >= FreeQuizDailyLimit {
			return false, quizCount, nil
		}
		config.DB.Exec(ctx, `UPDATE study_quota SET quiz_gens_today = quiz_gens_today + 1 WHERE user_id = $1`, userID)
		return true, quizCount + 1, nil
	}

	// flashcard: không giới hạn lần tạo, chỉ giới hạn số card
	return true, flashCount, nil
}

// ─── Flashcard APIs ───────────────────────────────────────────────────────────

// GenerateFlashcards tạo flashcard từ tài liệu bằng AI
// POST /api/study/docs/:doc_id/flashcards/generate
func GenerateFlashcards(c *gin.Context) {
	userID := c.GetString("user_id")
	docID := c.Param("doc_id")
	tier := c.GetString("tier") // "free" hoặc "pro"

	// Lấy top chunks của doc để làm ngữ liệu
	rows, err := config.DB.Query(config.Ctx, `
		SELECT content FROM document_chunks
		WHERE document_id = $1
		ORDER BY chunk_index ASC
		LIMIT 20`, docID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể lấy nội dung tài liệu"})
		return
	}
	defer rows.Close()

	var chunks []string
	for rows.Next() {
		var content string
		rows.Scan(&content)
		chunks = append(chunks, content)
	}
	if len(chunks) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Tài liệu chưa có dữ liệu, vui lòng đợi xử lý"})
		return
	}

	// Lấy title tài liệu
	var docTitle string
	config.DB.QueryRow(config.Ctx, `SELECT title FROM documents WHERE id = $1`, docID).Scan(&docTitle)

	// Giới hạn input cho prompt
	combinedText := strings.Join(chunks, "\n\n")
	if len(combinedText) > 8000 {
		combinedText = combinedText[:8000]
	}

	// Quyết định max card dựa trên tier
	maxCards := 20
	if tier == "pro" || tier == "ultra" {
		maxCards = 50
	}

	// AI Prompt tạo flashcard
	promptSys := fmt.Sprintf(`Bạn là chuyên gia sư phạm. Hãy tạo tối đa %d flashcard học tập từ nội dung tài liệu.
Trả về JSON hợp lệ, KHÔNG kèm markdown code block, theo đúng format sau:
[
  {"front": "Câu hỏi/Khái niệm ngắn gọn", "back": "Giải thích/Đáp án chi tiết", "difficulty": "easy|medium|hard", "topic": "Chủ đề cụ thể"}
]
Yêu cầu:
- front: Ngắn gọn (< 80 ký tự), dạng câu hỏi hoặc khái niệm cần nhớ
- back: Đầy đủ, rõ ràng, có thể dùng bullet points
- difficulty: Phân loại hợp lý dựa trên độ phức tạp
- topic: Chủ đề cụ thể trong tài liệu`, maxCards)

	messages := []utils.ChatMessage{
		{Role: "system", Content: promptSys},
		{Role: "user", Content: fmt.Sprintf("Tài liệu: %s\n\nNội dung:\n%s", docTitle, combinedText)},
	}

	// Dùng NineRouter → fallback Groq 70B
	fcMessages := []utils.ChatMessage{}
	fcMessages = append(fcMessages, messages...)

	rawJSON, _, err := utils.AI.ChatNonStream(utils.ServiceClassify, fcMessages)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI không thể tạo flashcard lúc này, vui lòng thử lại"})
		return
	}

	// Parse JSON từ AI
	rawJSON = strings.TrimSpace(rawJSON)
	// Loại bỏ markdown code block nếu AI trả về
	rawJSON = strings.TrimPrefix(rawJSON, "```json")
	rawJSON = strings.TrimPrefix(rawJSON, "```")
	rawJSON = strings.TrimSuffix(rawJSON, "```")
	rawJSON = strings.TrimSpace(rawJSON)

	type FlashcardItem struct {
		Front      string `json:"front"`
		Back       string `json:"back"`
		Difficulty string `json:"difficulty"`
		Topic      string `json:"topic"`
	}

	var cards []FlashcardItem
	if err := json.Unmarshal([]byte(rawJSON), &cards); err != nil {
		log.Printf("❌ [Flashcard] JSON parse error: %v\nRaw: %s", err, rawJSON[:min(200, len(rawJSON))])
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI trả về định dạng không hợp lệ, vui lòng thử lại"})
		return
	}

	// Giới hạn số card cho free user
	if tier == "free" && len(cards) > FreeFlashcardLimit {
		cards = cards[:FreeFlashcardLimit]
	}

	// Lưu vào DB
	setID := uuid.New().String()
	setTitle := fmt.Sprintf("Flashcard — %s", docTitle)
	_, err = config.DB.Exec(config.Ctx, `
		INSERT INTO flashcard_sets (id, doc_id, user_id, title, card_count)
		VALUES ($1, $2, $3, $4, $5)`,
		setID, docID, userID, setTitle, len(cards))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể lưu flashcard set"})
		return
	}

	for i, card := range cards {
		difficulty := card.Difficulty
		if difficulty != "easy" && difficulty != "medium" && difficulty != "hard" {
			difficulty = "medium"
		}
		_, err = config.DB.Exec(config.Ctx, `
			INSERT INTO flashcards (set_id, front, back, difficulty, topic, position)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			setID, card.Front, card.Back, difficulty, card.Topic, i)
		if err != nil {
			log.Printf("⚠️ [Flashcard] Failed to insert card %d: %v", i, err)
		}
	}

	log.Printf("✅ [Flashcard] Generated %d cards for doc %s (user: %s)", len(cards), docID, userID)
	c.JSON(http.StatusOK, gin.H{
		"success":  true,
		"set_id":   setID,
		"count":    len(cards),
		"is_capped": tier == "free" && len(cards) >= FreeFlashcardLimit,
	})
}

// GetFlashcardSets lấy danh sách flashcard sets của user cho một tài liệu
// GET /api/study/docs/:doc_id/flashcards
func GetFlashcardSets(c *gin.Context) {
	userID := c.GetString("user_id")
	docID := c.Param("doc_id")

	rows, err := config.DB.Query(config.Ctx, `
		SELECT id, title, card_count, created_at
		FROM flashcard_sets
		WHERE doc_id = $1 AND user_id = $2
		ORDER BY created_at DESC`, docID, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Query error"})
		return
	}
	defer rows.Close()

	type SetInfo struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		CardCount int       `json:"card_count"`
		CreatedAt time.Time `json:"created_at"`
	}

	var sets []SetInfo
	for rows.Next() {
		var s SetInfo
		rows.Scan(&s.ID, &s.Title, &s.CardCount, &s.CreatedAt)
		sets = append(sets, s)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": sets})
}

// GetFlashcards lấy tất cả flashcards trong một set
// GET /api/study/flashcards/:set_id
func GetFlashcards(c *gin.Context) {
	userID := c.GetString("user_id")
	setID := c.Param("set_id")

	// Kiểm tra quyền: set phải thuộc user này
	var ownerID string
	config.DB.QueryRow(config.Ctx, `SELECT user_id FROM flashcard_sets WHERE id = $1`, setID).Scan(&ownerID)
	if ownerID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Permission denied"})
		return
	}

	rows, err := config.DB.Query(config.Ctx, `
		SELECT id, front, back, difficulty, topic, position, remembered
		FROM flashcards
		WHERE set_id = $1
		ORDER BY position ASC`, setID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Query error"})
		return
	}
	defer rows.Close()

	type Card struct {
		ID         string  `json:"id"`
		Front      string  `json:"front"`
		Back       string  `json:"back"`
		Difficulty string  `json:"difficulty"`
		Topic      *string `json:"topic"`
		Position   int     `json:"position"`
		Remembered bool    `json:"remembered"`
	}

	var cards []Card
	for rows.Next() {
		var card Card
		rows.Scan(&card.ID, &card.Front, &card.Back, &card.Difficulty, &card.Topic, &card.Position, &card.Remembered)
		cards = append(cards, card)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": cards})
}

// MarkFlashcard đánh dấu card đã nhớ/chưa nhớ
// PATCH /api/study/flashcards/:card_id/mark
func MarkFlashcard(c *gin.Context) {
	userID := c.GetString("user_id")
	cardID := c.Param("card_id")

	var req struct {
		Remembered bool `json:"remembered"`
	}
	c.ShouldBindJSON(&req)

	// Kiểm tra quyền thông qua set
	var ownerID string
	config.DB.QueryRow(config.Ctx,
		`SELECT fs.user_id FROM flashcards f JOIN flashcard_sets fs ON fs.id = f.set_id WHERE f.id = $1`,
		cardID).Scan(&ownerID)
	if ownerID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Permission denied"})
		return
	}

	_, err := config.DB.Exec(config.Ctx,
		`UPDATE flashcards SET remembered = $1 WHERE id = $2`, req.Remembered, cardID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể cập nhật"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// ─── Quiz APIs ────────────────────────────────────────────────────────────────

type QuizConfig struct {
	NumQuestions int    `json:"num_questions"` // 5, 10, 20
	Type         string `json:"type"`          // "mcq", "essay", "mix"
	Difficulty   string `json:"difficulty"`    // "easy", "medium", "hard", "mix"
	Topic        string `json:"topic"`         // optional filter
}

// GenerateQuiz tạo quiz từ tài liệu bằng AI
// POST /api/study/docs/:doc_id/quiz/generate
func GenerateQuiz(c *gin.Context) {
	userID := c.GetString("user_id")
	docID := c.Param("doc_id")
	tier := c.GetString("tier")

	// 1. Kiểm tra quota (1 quiz/ngày cho free user)
	if tier == "free" || tier == "" {
		allowed, count, err := checkStudyQuota(userID, "quiz")
		if err != nil {
			log.Printf("⚠️ [Quiz] Quota check error for user %s: %v", userID, err)
		}
		if !allowed {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":      "QUOTA_EXCEEDED",
				"message":    fmt.Sprintf("Bạn đã tạo %d quiz hôm nay. Hạn mức free là %d quiz/ngày. Nâng cấp Pro để tạo không giới hạn!", count, FreeQuizDailyLimit),
				"quota_used": count,
				"quota_max":  FreeQuizDailyLimit,
			})
			return
		}
	}

	// 2. Đọc config từ body
	var cfg QuizConfig
	if err := c.ShouldBindJSON(&cfg); err != nil || cfg.NumQuestions == 0 {
		cfg = QuizConfig{NumQuestions: 10, Type: "mcq", Difficulty: "mix"}
	}
	if cfg.NumQuestions > 20 {
		cfg.NumQuestions = 20
	}

	// 3. Lấy chunks của doc
	rows, err := config.DB.Query(config.Ctx, `
		SELECT content FROM document_chunks
		WHERE document_id = $1
		ORDER BY chunk_index ASC LIMIT 20`, docID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể lấy nội dung"})
		return
	}
	defer rows.Close()

	var chunks []string
	for rows.Next() {
		var content string
		rows.Scan(&content)
		chunks = append(chunks, content)
	}

	var docTitle string
	config.DB.QueryRow(config.Ctx, `SELECT title FROM documents WHERE id = $1`, docID).Scan(&docTitle)

	combinedText := strings.Join(chunks, "\n\n")
	if len(combinedText) > 8000 {
		combinedText = combinedText[:8000]
	}

	// 4. Build AI prompt theo type
	var promptSys string
	if cfg.Type == "essay" {
		promptSys = fmt.Sprintf(`Bạn là chuyên gia giáo dục. Tạo %d câu hỏi tự luận ngắn từ tài liệu.
Trả về JSON hợp lệ (KHÔNG có markdown):
[{"question":"...","type":"essay","difficulty":"%s","explanation":"Gợi ý đáp án/rubric chấm điểm ngắn gọn"}]`, cfg.NumQuestions, cfg.Difficulty)
	} else {
		promptSys = fmt.Sprintf(`Bạn là chuyên gia giáo dục. Tạo %d câu hỏi trắc nghiệm MCQ từ tài liệu.
Trả về JSON hợp lệ (KHÔNG có markdown):
[{"question":"...","type":"mcq","options":["A","B","C","D"],"correct_index":0,"explanation":"Giải thích đáp án","difficulty":"%s"}]
Yêu cầu:
- Mỗi câu có đúng 4 lựa chọn (A,B,C,D)
- correct_index: 0=A, 1=B, 2=C, 3=D
- Distractor hợp lý, không quá rõ ràng
- explanation: Giải thích ngắn gọn tại sao đáp án đúng`, cfg.NumQuestions, cfg.Difficulty)
	}

	messages := []utils.ChatMessage{
		{Role: "system", Content: promptSys},
		{Role: "user", Content: fmt.Sprintf("Tài liệu: %s\n\nNội dung:\n%s", docTitle, combinedText)},
	}

	// Dùng NineRouter (Sonnet-class) → fallback Groq 70B thông qua ServiceChat (reasoning tốt hơn)
	rawJSON, _, err := utils.AI.ChatNonStream(utils.ServiceChat, messages)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI không thể tạo quiz lúc này"})
		return
	}

	// 5. Parse JSON
	rawJSON = strings.TrimSpace(rawJSON)
	rawJSON = strings.TrimPrefix(rawJSON, "```json")
	rawJSON = strings.TrimPrefix(rawJSON, "```")
	rawJSON = strings.TrimSuffix(rawJSON, "```")
	rawJSON = strings.TrimSpace(rawJSON)

	type QuizItem struct {
		Question     string   `json:"question"`
		Type         string   `json:"type"`
		Options      []string `json:"options"`
		CorrectIndex *int     `json:"correct_index"`
		Explanation  string   `json:"explanation"`
		Difficulty   string   `json:"difficulty"`
	}

	var items []QuizItem
	if err := json.Unmarshal([]byte(rawJSON), &items); err != nil {
		log.Printf("❌ [Quiz] JSON parse error: %v\nRaw: %s", err, rawJSON[:min(200, len(rawJSON))])
		c.JSON(http.StatusInternalServerError, gin.H{"error": "AI trả về định dạng không hợp lệ"})
		return
	}

	// 6. Lưu quiz vào DB
	cfgBytes, _ := json.Marshal(cfg)
	quizID := uuid.New().String()
	_, err = config.DB.Exec(config.Ctx, `
		INSERT INTO quizzes (id, doc_id, user_id, title, config)
		VALUES ($1, $2, $3, $4, $5)`,
		quizID, docID, userID,
		fmt.Sprintf("Quiz — %s", docTitle),
		string(cfgBytes))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể lưu quiz"})
		return
	}

	for pos, item := range items {
		optBytes, _ := json.Marshal(item.Options)
		_, err = config.DB.Exec(config.Ctx, `
			INSERT INTO quiz_questions (quiz_id, type, question, options, correct_index, explanation, position)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			quizID, item.Type, item.Question, string(optBytes),
			item.CorrectIndex, item.Explanation, pos)
		if err != nil {
			log.Printf("⚠️ [Quiz] Failed to insert question %d: %v", pos, err)
		}
	}

	log.Printf("✅ [Quiz] Generated quiz %s with %d questions for doc %s", quizID, len(items), docID)
	c.JSON(http.StatusOK, gin.H{
		"success":       true,
		"quiz_id":       quizID,
		"question_count": len(items),
	})
}

// GetQuiz lấy quiz và danh sách câu hỏi (không có correct_index cho MCQ)
// GET /api/study/quiz/:quiz_id
func GetQuiz(c *gin.Context) {
	userID := c.GetString("user_id")
	quizID := c.Param("quiz_id")

	// Kiểm tra quyền
	var ownerID, title string
	var cfgJSON []byte
	err := config.DB.QueryRow(config.Ctx,
		`SELECT user_id, title, config FROM quizzes WHERE id = $1`, quizID).
		Scan(&ownerID, &title, &cfgJSON)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Quiz không tồn tại"})
		return
	}
	if ownerID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Permission denied"})
		return
	}

	// Lấy câu hỏi — KHÔNG trả về correct_index
	rows, err := config.DB.Query(config.Ctx, `
		SELECT id, type, question, options, position
		FROM quiz_questions
		WHERE quiz_id = $1
		ORDER BY position ASC`, quizID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Query error"})
		return
	}
	defer rows.Close()

	type QuestionPublic struct {
		ID       string          `json:"id"`
		Type     string          `json:"type"`
		Question string          `json:"question"`
		Options  json.RawMessage `json:"options,omitempty"`
		Position int             `json:"position"`
	}

	var questions []QuestionPublic
	for rows.Next() {
		var q QuestionPublic
		var opts []byte
		rows.Scan(&q.ID, &q.Type, &q.Question, &opts, &q.Position)
		q.Options = json.RawMessage(opts)
		questions = append(questions, q)
	}

	c.JSON(http.StatusOK, gin.H{
		"success":   true,
		"quiz_id":   quizID,
		"title":     title,
		"config":    json.RawMessage(cfgJSON),
		"questions": questions,
	})
}

// SubmitQuiz nhận bài làm, tính điểm MCQ ngay, grading essay async
// POST /api/study/quiz/:quiz_id/submit
func SubmitQuiz(c *gin.Context) {
	userID := c.GetString("user_id")
	quizID := c.Param("quiz_id")

	var req struct {
		Answers      []map[string]interface{} `json:"answers"` // [{question_id, answer}]
		TimeSpentSec int                      `json:"time_spent_sec"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	// Lấy tất cả câu hỏi kèm đáp án đúng
	rows, err := config.DB.Query(config.Ctx, `
		SELECT id, type, correct_index, explanation
		FROM quiz_questions WHERE quiz_id = $1`, quizID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Query error"})
		return
	}
	defer rows.Close()

	type QuestionAnswer struct {
		ID           string
		Type         string
		CorrectIndex *int
		Explanation  string
	}

	questionMap := map[string]QuestionAnswer{}
	for rows.Next() {
		var qa QuestionAnswer
		rows.Scan(&qa.ID, &qa.Type, &qa.CorrectIndex, &qa.Explanation)
		questionMap[qa.ID] = qa
	}

	// Chấm điểm MCQ
	type AnswerResult struct {
		QuestionID  string      `json:"question_id"`
		UserAnswer  interface{} `json:"user_answer"`
		IsCorrect   *bool       `json:"is_correct"`
		Score       float64     `json:"score"`
		Explanation string      `json:"explanation"`
	}

	var results []AnswerResult
	mcqCorrect := 0
	mcqTotal := 0

	for _, ans := range req.Answers {
		qID, _ := ans["question_id"].(string)
		userAnswer := ans["answer"]
		qa, ok := questionMap[qID]
		if !ok {
			continue
		}

		result := AnswerResult{
			QuestionID:  qID,
			UserAnswer:  userAnswer,
			Explanation: qa.Explanation,
		}

		if qa.Type == "mcq" && qa.CorrectIndex != nil {
			mcqTotal++
			userIdx, _ := userAnswer.(float64) // JSON numbers come as float64
			isCorrect := int(userIdx) == *qa.CorrectIndex
			result.IsCorrect = &isCorrect
			if isCorrect {
				mcqCorrect++
				result.Score = 10
			}
		}
		// Essay: score sẽ được grading async (để 0 trước)

		results = append(results, result)
	}

	// Tính điểm tổng MCQ
	var totalScore float64
	if mcqTotal > 0 {
		totalScore = float64(mcqCorrect) / float64(mcqTotal) * 100
	}

	// Lưu attempt
	resultsBytes, _ := json.Marshal(results)
	attemptID := uuid.New().String()
	_, err = config.DB.Exec(config.Ctx, `
		INSERT INTO quiz_attempts (id, quiz_id, user_id, answers, score, time_spent_sec)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		attemptID, quizID, userID, string(resultsBytes), totalScore, req.TimeSpentSec)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể lưu kết quả"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"attempt_id": attemptID,
		"score":      totalScore,
		"mcq_result": gin.H{"correct": mcqCorrect, "total": mcqTotal},
		"answers":    results,
	})
}

// GetMasteryScore tính mastery score 0-100% cho một tài liệu
// GET /api/study/docs/:doc_id/mastery
func GetMasteryScore(c *gin.Context) {
	userID := c.GetString("user_id")
	docID := c.Param("doc_id")

	// Flashcard mastery: tỷ lệ card đã nhớ
	var totalCards, rememberedCards int
	config.DB.QueryRow(config.Ctx, `
		SELECT COUNT(f.id), SUM(CASE WHEN f.remembered THEN 1 ELSE 0 END)
		FROM flashcards f
		JOIN flashcard_sets fs ON fs.id = f.set_id
		WHERE fs.doc_id = $1 AND fs.user_id = $2`, docID, userID).
		Scan(&totalCards, &rememberedCards)

	// Quiz mastery: avg score của các attempts
	var avgQuizScore float64
	config.DB.QueryRow(config.Ctx, `
		SELECT COALESCE(AVG(qa.score), 0)
		FROM quiz_attempts qa
		JOIN quizzes q ON q.id = qa.quiz_id
		WHERE q.doc_id = $1 AND qa.user_id = $2`, docID, userID).
		Scan(&avgQuizScore)

	// Tổng hợp: 50% flashcard + 50% quiz
	var flashcardScore float64
	if totalCards > 0 {
		flashcardScore = float64(rememberedCards) / float64(totalCards) * 100
	}

	masteryScore := (flashcardScore*0.5 + avgQuizScore*0.5)

	c.JSON(http.StatusOK, gin.H{
		"success":          true,
		"mastery_score":    masteryScore,
		"flashcard_score":  flashcardScore,
		"quiz_score":       avgQuizScore,
		"flashcard_stats":  gin.H{"total": totalCards, "remembered": rememberedCards},
	})
}

// ExportFlashcardsCSV export flashcard sang CSV
// GET /api/study/flashcards/:set_id/export?format=csv
func ExportFlashcardsCSV(c *gin.Context) {
	userID := c.GetString("user_id")
	setID := c.Param("set_id")

	// Kiểm tra quyền
	var ownerID, title string
	config.DB.QueryRow(config.Ctx,
		`SELECT user_id, title FROM flashcard_sets WHERE id = $1`, setID).
		Scan(&ownerID, &title)
	if ownerID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Permission denied"})
		return
	}

	rows, err := config.DB.Query(config.Ctx, `
		SELECT front, back, difficulty, COALESCE(topic, '')
		FROM flashcards WHERE set_id = $1 ORDER BY position ASC`, setID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Query error"})
		return
	}
	defer rows.Close()

	// Build CSV
	var sb strings.Builder
	sb.WriteString("Front,Back,Difficulty,Topic\n")
	for rows.Next() {
		var front, back, difficulty, topic string
		rows.Scan(&front, &back, &difficulty, &topic)
		// Escape CSV: bọc trong dấu nháy kép, escape dấu nháy kép trong nội dung
		escCSV := func(s string) string {
			return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
		}
		sb.WriteString(fmt.Sprintf("%s,%s,%s,%s\n",
			escCSV(front), escCSV(back), escCSV(difficulty), escCSV(topic)))
	}

	filename := strings.ReplaceAll(title, " ", "_") + ".csv"
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.String(http.StatusOK, sb.String())
}

// Utility
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
