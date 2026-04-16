package controllers

import (
	"context"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// AIResponseLogEntry là struct dùng để ghi log AI response
type AIResponseLogEntry struct {
	ID           string // UUID được generate sẵn bên ngoài (optional, nếu rỗng thì DB auto-gen)
	SessionID    string
	UserID       string
	DocumentID   string
	CollectionID string
	Question     string
	Answer       string
	ModelUsed    string
	LatencyMs    int
	TokenCount   int
	SourcesCount int
}


// SaveAIResponseLog ghi log vào DB (auto-gen ID). Giữ lại để tương thích backward.
func SaveAIResponseLog(entry AIResponseLogEntry) string {
	var logID string
	err := config.DB.QueryRow(context.Background(), `
		INSERT INTO ai_response_logs 
		  (session_id, user_id, document_id, collection_id, question, answer, model_used, latency_ms, token_count, sources_count)
		VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), $5, $6, $7, $8, $9, $10)
		RETURNING id`,
		entry.SessionID, entry.UserID, entry.DocumentID, entry.CollectionID,
		entry.Question, entry.Answer, entry.ModelUsed, entry.LatencyMs,
		entry.TokenCount, entry.SourcesCount,
	).Scan(&logID)
	if err != nil {
		log.Printf("❌ [AILog] Failed to save AI response log: %v", err)
		return ""
	}
	log.Printf("📊 [AILog] Saved log %s (model=%s, latency=%dms)", logID, entry.ModelUsed, entry.LatencyMs)
	if logID != "" {
		go labelTopicAsync(logID, entry.Question)
	}
	return logID
}

// SaveAIResponseLogWithID ghi log vào DB dùng UUID được tạo sẵn.
// Dùng khi cần biết log_id trước khi ghi DB (ví dụ: gửi log_id trong SSE event done)
func SaveAIResponseLogWithID(entry AIResponseLogEntry) {
	_, err := config.DB.Exec(context.Background(), `
		INSERT INTO ai_response_logs 
		  (id, session_id, user_id, document_id, collection_id, question, answer, model_used, latency_ms, token_count, sources_count)
		VALUES ($1, $2, $3, NULLIF($4,''), NULLIF($5,''), $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO NOTHING`,
		entry.ID,
		entry.SessionID, entry.UserID, entry.DocumentID, entry.CollectionID,
		entry.Question, entry.Answer, entry.ModelUsed, entry.LatencyMs,
		entry.TokenCount, entry.SourcesCount,
	)
	if err != nil {
		log.Printf("❌ [AILog] Failed to save AI response log (id=%s): %v", entry.ID, err)
		return
	}
	log.Printf("📊 [AILog] Saved log %s (model=%s, latency=%dms)", entry.ID, entry.ModelUsed, entry.LatencyMs)
	go labelTopicAsync(entry.ID, entry.Question)
}


// labelTopicAsync phân loại chủ đề câu hỏi bằng AI nhẹ (Gemini Flash / Groq)
func labelTopicAsync(logID, question string) {
	messages := []utils.ChatMessage{
		{Role: "system", Content: `Bạn là hệ thống phân loại câu hỏi học thuật. 
Phân loại câu hỏi vào MỘT trong các loại sau (chỉ trả về đúng từ khóa, không giải thích):
- "định nghĩa" — câu hỏi hỏi về khái niệm, thuật ngữ
- "so sánh" — câu hỏi đối chiếu, phân biệt hai hay nhiều thứ
- "tính toán" — câu hỏi yêu cầu tính toán, công thức
- "tóm tắt" — câu hỏi yêu cầu tổng hợp, tóm lược
- "phân tích" — câu hỏi yêu cầu lập luận sâu, giải thích nguyên nhân
- "thực hành" — câu hỏi về cách làm, ví dụ thực tế
- "khác" — không thuộc các loại trên`},
		{Role: "user", Content: fmt.Sprintf("Phân loại câu hỏi sau: %s", question)},
	}

	label, _, err := utils.AI.ChatNonStream(utils.ServiceClassify, messages)
	if err != nil {
		log.Printf("⚠️ [TopicLabel] Failed to label log %s: %v", logID, err)
		return
	}

	// Clean label
	validLabels := map[string]bool{
		"định nghĩa": true, "so sánh": true, "tính toán": true,
		"tóm tắt": true, "phân tích": true, "thực hành": true, "khác": true,
	}
	if !validLabels[label] {
		label = "khác"
	}

	_, err = config.DB.Exec(context.Background(),
		`UPDATE ai_response_logs SET topic_label = $1 WHERE id = $2`, label, logID)
	if err != nil {
		log.Printf("⚠️ [TopicLabel] Failed to update label for log %s: %v", logID, err)
	} else {
		log.Printf("🏷️ [TopicLabel] Log %s labeled as: %s", logID, label)
	}
}

// RatingRequest là body của API POST /api/feedback/rating
type RatingRequest struct {
	LogID   string `json:"log_id" binding:"required"`
	Thumbs  *bool  `json:"thumbs" binding:"required"`
	Rating  *int   `json:"rating"` // optional 1-5
	Comment string `json:"comment"` // optional
}

// SubmitResponseRating lưu đánh giá của user cho một AI response (thumbs up/down)
// Route: POST /api/feedback/rating
func SubmitResponseRating(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var req RatingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Thiếu log_id hoặc thumbs"})
		return
	}

	// Validate log_id tồn tại
	var exists bool
	err := config.DB.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM ai_response_logs WHERE id = $1)`, req.LogID).Scan(&exists)
	if err != nil || !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Log không tồn tại"})
		return
	}

	// Upsert rating (user có thể đổi ý)
	_, err = config.DB.Exec(context.Background(), `
		INSERT INTO response_ratings (log_id, user_id, thumbs, rating, comment)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (log_id, user_id)
		DO UPDATE SET thumbs = $3, rating = $4, comment = $5, created_at = NOW()`,
		req.LogID,
		userID,
		*req.Thumbs,
		func() interface{} {
			if req.Rating != nil {
				return *req.Rating
			}
			return nil
		}(),
		func() interface{} {
			if req.Comment != "" {
				return req.Comment
			}
			return nil
		}(),
	)
	if err != nil {
		log.Printf("❌ [Rating] Failed to save rating for log %s: %v", req.LogID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể lưu đánh giá"})
		return
	}

	thumbsStr := "👍"
	if !*req.Thumbs {
		thumbsStr = "👎"
	}
	log.Printf("⭐ [Rating] User %s rated log %s: %s", userID, req.LogID, thumbsStr)

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Đã lưu đánh giá"})
}

// GetRatingByLogID lấy rating hiện tại của user cho một log (để FE hiển thị trạng thái active)
// Route: GET /api/feedback/rating/:log_id
func GetRatingByLogID(c *gin.Context) {
	userID := c.GetString("user_id")
	logID := c.Param("log_id")

	var rating struct {
		Thumbs  bool      `json:"thumbs"`
		Rating  *int      `json:"rating"`
		Comment string    `json:"comment"`
		Created time.Time `json:"created_at"`
	}

	err := config.DB.QueryRow(context.Background(), `
		SELECT thumbs, rating, COALESCE(comment, ''), created_at
		FROM response_ratings
		WHERE log_id = $1 AND user_id = $2`, logID, userID).
		Scan(&rating.Thumbs, &rating.Rating, &rating.Comment, &rating.Created)

	if err != nil {
		// Chưa có rating thì trả về null (không phải lỗi)
		c.JSON(http.StatusOK, gin.H{"data": nil})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": rating})
}
