package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"mindex-backend/config"
	"strings"
	"time"
)

// ambiguousReferenceTokens là các đại từ/từ chỉ định mà nếu xuất hiện trong câu hỏi
// thì cần dùng history để làm rõ. Nếu không có, query đã tự đủ nghĩa.
var ambiguousReferenceTokens = []string{
	// Tiếng Việt — đại từ / từ chỉ định
	"nó", "cái đó", "chúng", "họ", "điều này", "điều đó", "cái này", "cái kia",
	"phương pháp đó", "khái niệm đó", "khái niệm trên", "phương pháp trên",
	"vấn đề đó", "vấn đề này", "ý đó", "ý trên", "bước đó", "bước này",
	"ví dụ đó", "ví dụ trên", "loại đó", "loại này", "mô hình đó", "mô hình trên",
	"tiếp theo", "còn về", "còn cái", "thế còn", "vậy còn", "còn gì",
	// Tiếng Việt — connector xã giao cần strip + rewrite dựa trên history
	"vậy thì", "thế thì", "vậy còn", "thế còn",
	"theo bạn", "bạn có nghĩ", "bạn thấy", "theo ý bạn", "ý bạn",
	// English
	"it ", "they ", "them ", "that ", "this ", "those ", "these ",
	"the above", "the previous", "mentioned above", "as above",
}

// socialLeadins là các tiền tố xã giao/connector không mang ngữ nghĩa tìm kiếm.
// StripSocialLeadin loại bỏ chúng khỏi query TRƯỚC KHI embed — chỉ dùng cho searchQuery,
// không dùng cho câu hỏi gốc gửi sang AI.
var socialLeadins = []string{
	// Đồng ý / thừa nhận
	"ok, ", "oke, ", "okay, ", "được rồi, ", "được rồi. ",
	"ừ, ", "ừ nhỉ, ", "ah, ", "à, ",
	// Cảm ơn
	"cảm ơn bạn, ", "cảm ơn, ", "cảm ơn nhé, ",
	"thanks, ", "thank you, ",
	// Connector follow-up
	"vậy thì ", "vậy thì, ", "thế thì ", "thế thì, ",
	"vậy còn ", "thế còn ",
	// Xin ý kiến (tiền tố — phần sau mới là câu hỏi thực)
	"theo bạn, ", "theo bạn ", "bạn thấy, ", "bạn thấy ",
	"bạn có nghĩ ", "theo ý bạn, ", "theo ý bạn ",
}

// StripSocialLeadin loại bỏ các tiền tố xã giao không mang ngữ nghĩa ra khỏi câu hỏi.
// Kết quả chỉ dùng cho vector embedding / hybrid search — không thay thế câu hỏi gốc.
func StripSocialLeadin(question string) string {
	q := strings.TrimSpace(question)
	lower := strings.ToLower(q)
	for _, lead := range socialLeadins {
		if strings.HasPrefix(lower, lead) {
			q = strings.TrimSpace(q[len(lead):])
			lower = lower[len(lead):]
		}
	}
	// Nếu sau khi strip còn lại rỗng, trả về câu gốc để không mất thông tin
	if q == "" {
		return question
	}
	return q
}

// RewriteQueryWithHistory sử dụng LLM để biến câu hỏi của user thành một query độc lập dựa trên lịch sử chat (SYS-023)
func RewriteQueryWithHistory(userQuestion string, historySummary string) string {
	if historySummary == "" {
		return userQuestion
	}

	// Nếu query không chứa đại từ/từ chỉ định mơ hồ → đã tự đủ nghĩa, không cần LLM
	lowerQ := strings.ToLower(strings.TrimSpace(userQuestion))
	needsRewrite := false
	for _, token := range ambiguousReferenceTokens {
		if strings.Contains(lowerQ, token) {
			needsRewrite = true
			break
		}
	}
	if !needsRewrite {
		return userQuestion
	}

	sysPrompt := `Bạn là công cụ rewrite câu hỏi cho hệ thống tìm kiếm tài liệu.

Nhiệm vụ: Rewrite câu hỏi thành một câu độc lập, đủ ngữ cảnh để tìm kiếm trong cơ sở dữ liệu tài liệu mà không cần đọc lịch sử hội thoại.

QUY TẮC:
1. Thay thế đại từ mơ hồ bằng tên cụ thể từ lịch sử:
 "nó", "cái đó", "chúng", "phương pháp đó", "khái niệm trên" → tên thật
2. Nếu câu hỏi đã rõ và độc lập: trả về NGUYÊN VĂN, không sửa.
3. Chỉ dùng thông tin có trong <history> và <question>. Không thêm thông tin từ kiến thức bên ngoài.
4. Chỉ trả về câu query đã rewrite. Không giải thích, không prefix.`

	userContent := fmt.Sprintf(`
Lịch sử hội thoại gần nhất (tối đa 3 lượt):
<history>
%s
</history>

Câu hỏi hiện tại của user:
<question>
%s
</question>
`, historySummary, userQuestion)

	messages := []ChatMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userContent},
	}

	// Sử dụng ServiceSearch (thường là model nhanh/rẻ như Gemini Flash hoặc Llama 8B)
	rewritten, usedProvider, err := AI.ChatNonStream(ServiceSearch, messages)
	if err != nil || rewritten == "" {
		log.Printf("⚠️ [QueryRewriter] Failed via %s: %v. Using original query.", usedProvider, err)
		return userQuestion
	}

	log.Printf("💡 [QueryRewriter] %s -> %s (via %s)", userQuestion, rewritten, usedProvider)
	return rewritten
}

// ─── Intent Classifier (Fix 6) ───────────────────────────────────────────────

// QuestionIntent phân loại kiểu câu hỏi trước khi chọn chiến lược embedding/search.
type QuestionIntent int

const (
	IntentDirect        QuestionIntent = iota // thẳng vào nội dung tài liệu
	IntentTangential                          // liên quan chủ đề, có thể nằm ngoài tài liệu
	IntentOpinion                             // hỏi ý kiến / đánh giá / so sánh
	IntentConversational                      // tiền tố xã giao bọc ngoài câu hỏi thực
	IntentOOT                                 // ngoài phạm vi (dự phòng — OOT check chính chạy sớm hơn)
)

func (i QuestionIntent) String() string {
	labels := [...]string{"direct", "tangential", "opinion", "conversational", "oot"}
	if int(i) < len(labels) {
		return labels[i]
	}
	return "unknown"
}

// opinionMarkers là các cụm từ chỉ ra câu hỏi dạng ý kiến / đánh giá / khuyến nghị.
// Khi phát hiện, hệ thống dùng HyDE thay vì embed câu hỏi trực tiếp.
var opinionMarkers = []string{
	// Yêu cầu ý kiến trực tiếp
	"theo bạn", "bạn có nghĩ", "bạn thấy", "theo ý bạn", "ý bạn",
	"bạn cho rằng", "bạn đánh giá", "bạn nhận xét",
	// Gợi ý / khuyến nghị
	"có nên", "nên dùng", "nên chọn", "nên học", "nên sử dụng",
	"có đáng", "có đáng học", "có đáng dùng", "có đáng dùng không",
	// So sánh / đánh giá
	"tốt hơn", "hay hơn", "tốt không", "tốt hơn không",
	"có hợp", "có phù hợp", "có hiệu quả", "hiệu quả không",
	"có ổn không", "có ok không", "có tốt không",
	// English
	"do you think", "what do you think", "should i", "is it worth",
	"is it good", "better than", "worth learning", "recommend",
	"good for", "suitable for",
}

// ClassifyIntent phân loại intent câu hỏi bằng heuristic thuần — zero LLM call.
// Nhận câu hỏi GỐC (trước StripSocialLeadin) để giữ đủ tín hiệu phân loại.
func ClassifyIntent(question, historySummary string) QuestionIntent {
	lower := strings.ToLower(strings.TrimSpace(question))

	// Opinion: chứa marker hỏi ý kiến / đánh giá / khuyến nghị
	for _, m := range opinionMarkers {
		if strings.Contains(lower, m) {
			return IntentOpinion
		}
	}

	// Conversational: bắt đầu bằng tiền tố xã giao (socialLeadins đã định nghĩa ở trên)
	for _, lead := range socialLeadins {
		if strings.HasPrefix(lower, lead) {
			return IntentConversational
		}
	}

	// Tangential: đang trong phiên có lịch sử → câu hỏi tiếp theo likely follow-up
	if historySummary != "" {
		return IntentTangential
	}

	return IntentDirect
}

// ─── HyDE — Hypothetical Document Embeddings (Fix 5) ─────────────────────────

// GenerateHyDE tạo đoạn văn "câu trả lời giả" từ câu hỏi để dùng cho vector embedding.
//
// Vấn đề HyDE giải quyết: câu hỏi dạng ý kiến / gián tiếp nằm ở "question space" trong
// embedding space, trong khi document chunks nằm ở "answer space" — khoảng cách cosine
// giữa hai không gian này khiến retrieval kém chính xác dù nội dung liên quan.
// HyDE bridge khoảng cách đó bằng cách embed "câu trả lời giả" thay vì câu hỏi.
//
// Kết quả CHỈ dùng để tạo queryVec cho vector search — không truyền vào prompt AI.
func GenerateHyDE(question string) string {
	if AI == nil {
		return question
	}

	sysPrompt := `Bạn là công cụ tạo đoạn văn giả cho hệ thống tìm kiếm tài liệu (kỹ thuật HyDE).

Nhiệm vụ: Tạo một đoạn văn ngắn (2-3 câu) mô phỏng nội dung có thể xuất hiện trong tài liệu kỹ thuật/học thuật để trả lời câu hỏi sau.

Quy tắc:
1. Viết theo dạng KHẲNG ĐỊNH như đoạn trích từ tài liệu, không phải lời AI
2. Ngắn gọn 2-3 câu, dùng thuật ngữ kỹ thuật phù hợp
3. Không bắt đầu bằng "Theo tôi", "Tôi nghĩ", "Dựa trên"
4. Chỉ trả về đoạn text thuần, không prefix, không giải thích`

	messages := []ChatMessage{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: question},
	}

	hypo, _, err := AI.ChatNonStream(ServiceSearch, messages)
	if err != nil || strings.TrimSpace(hypo) == "" {
		log.Printf("⚠️ [HyDE] Generation failed: %v. Falling back to original query.", err)
		return question
	}

	runes := []rune(strings.TrimSpace(hypo))
	preview := string(runes)
	if len(runes) > 80 {
		preview = string(runes[:80]) + "..."
	}
	log.Printf("💭 [HyDE] Generated hypothetical for %q -> %q", question, preview)
	return strings.TrimSpace(hypo)
}

// ─── Session Topic Vector (Fix 7) ────────────────────────────────────────────

const (
	topicVecKeyPrefix = "topic:"
	topicVecTTL       = 2 * time.Hour
	// α: trọng số cho vector mới trong rolling average (30% mới, 70% cũ)
	topicVecAlpha = 0.30
	// Ngưỡng: câu hỏi được coi là "in-topic theo session" nếu similarity >= giá trị này
	TopicSimBypassThreshold = 0.52
)

// CosineSim tính cosine similarity giữa hai vector float32.
func CosineSim(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		fa, fb := float64(a[i]), float64(b[i])
		dot += fa * fb
		normA += fa * fa
		normB += fb * fb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// GetSessionTopicSim lấy topic vector của phiên từ Redis và trả về cosine similarity
// với queryVec. Trả về 0 nếu Redis không khả dụng hoặc phiên chưa có topic vector.
func GetSessionTopicSim(ctx context.Context, sessionID string, queryVec []float32) float64 {
	if config.RedisClient == nil || len(queryVec) == 0 {
		return 0
	}
	key := topicVecKeyPrefix + sessionID
	raw, err := config.RedisClient.Get(ctx, key).Bytes()
	if err != nil {
		return 0 // key chưa tồn tại hoặc Redis lỗi — không block
	}
	var topicVec []float32
	if err := json.Unmarshal(raw, &topicVec); err != nil {
		return 0
	}
	return CosineSim(queryVec, topicVec)
}

// UpdateSessionTopicVec cập nhật topic vector của phiên bằng rolling average.
// Gọi async (goroutine) sau khi câu hỏi được xác nhận là in-scope — không block response.
func UpdateSessionTopicVec(ctx context.Context, sessionID string, newVec []float32) {
	if config.RedisClient == nil || len(newVec) == 0 {
		return
	}
	key := topicVecKeyPrefix + sessionID

	// Đọc topic vec hiện tại (nếu có)
	raw, err := config.RedisClient.Get(ctx, key).Bytes()
	var updated []float32
	if err != nil || raw == nil {
		// Lần đầu tiên — dùng chính queryVec làm topic seed
		updated = newVec
	} else {
		var existing []float32
		if jsonErr := json.Unmarshal(raw, &existing); jsonErr != nil || len(existing) != len(newVec) {
			updated = newVec
		} else {
			// Rolling average: 70% cũ + 30% mới
			updated = make([]float32, len(newVec))
			for i := range newVec {
				updated[i] = float32((1-topicVecAlpha)*float64(existing[i]) + topicVecAlpha*float64(newVec[i]))
			}
		}
	}

	encoded, err := json.Marshal(updated)
	if err != nil {
		log.Printf("⚠️ [TopicVec] Marshal failed session=%s: %v", sessionID, err)
		return
	}
	if err := config.RedisClient.Set(ctx, key, encoded, topicVecTTL).Err(); err != nil {
		log.Printf("⚠️ [TopicVec] Redis set failed session=%s: %v", sessionID, err)
	}
}
