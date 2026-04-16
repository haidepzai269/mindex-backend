package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"regexp"
	"strings"
)

type ModerationResult struct {
	Passed      bool   `json:"passed"`
	Reason      string `json:"reason"`
	Tier        int    `json:"tier"`
	SubjectArea string `json:"subject_area"`
}

var (
	spamURLRegex    = regexp.MustCompile(`(bit\.ly|t\.co|tinyurl\.com|cutt\.ly|shurte\.st|goo\.gl|ow\.ly|is\.gd|buff\.ly|bit\.do)`)
	phoneRegex      = regexp.MustCompile(`(0|\+84)(\s|\.)?((3[2-9])|(5[689])|(7[06-9])|(8[1-689])|(9[0-46-9]))(\d)(\s|\.)?(\d{3})(\s|\.)?(\d{3})`)
	redFlagKeywords = []string{
		"giá chỉ", "liên hệ ngay", "ưu đãi hôm nay", "mua ngay", "giá rẻ nhất",
		"click here", "free download", "tải ngay", "trực tiếp bóng đá", "kèo nhà cái",
		"xổ số", "soi cầu", "kiếm tiền online", "tuyển dụng gấp", "làm việc tại nhà",
		"nhận quà", "trúng thưởng", "khuyến mãi cực lớn", "duy nhất hôm nay",
	}
)

// Tier 1: Rule-based checks
func T1RuleBased(ctx context.Context, fileHash string, tokenCount int, charCount int, rawText string) (bool, string) {
	// Check rejected hashes
	var exists bool
	err := config.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM rejected_hashes WHERE file_hash = $1)", fileHash).Scan(&exists)
	if err == nil && exists {
		return false, "File đã bị từ chối trước đó (Blacklisted)"
	}

	if tokenCount < 50 {
		return false, fmt.Sprintf("Tài liệu quá ít nội dung (Có %d tokens, yêu cầu tối thiểu 50)", tokenCount)
	}
	if charCount < 200 {
		return false, fmt.Sprintf("Tài liệu không đủ độ dài hoặc không đọc được (Có %d ký tự, yêu cầu tối thiểu 200)", charCount)
	}

	if spamURLRegex.MatchString(rawText) {
		return false, "Phát hiện link spam hoặc rút gọn trái phép"
	}

	// Đếm số lượng số điện thoại
	phoneMatches := phoneRegex.FindAllString(rawText, -1)
	if len(phoneMatches) > 3 {
		return false, "Phát hiện quá nhiều số điện thoại (Nghi vấn quảng cáo)"
	}

	return true, ""
}

// Tier 2: Lightweight Keyword checks
func T2KeywordCheck(rawText string) (bool, string) {
	// Chỉ check 200 từ đầu tiên
	words := strings.Fields(rawText)
	limit := 200
	if len(words) < limit {
		limit = len(words)
	}
	first200Content := strings.ToLower(strings.Join(words[:limit], " "))

	foundCount := 0
	for _, kw := range redFlagKeywords {
		if strings.Contains(first200Content, strings.ToLower(kw)) {
			foundCount++
		}
		if foundCount >= 2 {
			return false, fmt.Sprintf("Phát hiện từ khóa vi phạm: %s", kw)
		}
	}

	return true, ""
}

// Tier 3: AI Check via Groq
func T3AICheck(rawText string) (bool, string, string) {
	// Lấy 500 từ đầu tiên
	words := strings.Fields(rawText)
	limit := 500
	if len(words) < limit {
		limit = len(words)
	}
	sampleText := strings.Join(words[:limit], " ")

	// Làm sạch text để tránh lỗi JSON hoặc ký tự lạ khi gửi sang AI
	cleanSample := regexp.MustCompile(`[\r\n\t]+`).ReplaceAllString(sampleText, " ")
	cleanSample = regexp.MustCompile(`[^\p{L}\p{N}\p{P}\s]+`).ReplaceAllString(cleanSample, "")
	runes := []rune(cleanSample)
	if len(runes) > 2000 {
		cleanSample = string(runes[:2000])
	}

	prompt := fmt.Sprintf(`Phân tích 500 từ đầu tiên của tài liệu này và trả về JSON duy nhất:
{
  "is_academic": boolean, 
  "quality_score": number (1-10), 
  "subject_area": string
}
Lưu ý:
- Học thuật (is_academic = true) bao gồm: giáo trình, báo cáo khoa học, tiểu luận, hướng dẫn kỹ thuật.
- Từ chối nếu là văn bản rác, quảng cáo, hoặc nội dung không mang tính giáo dục.

Nội dung: %s`, cleanSample)

	messages := []ChatMessage{
		{Role: "system", Content: "Bạn là một AI chuyên phân loại tài liệu học thuật cho sinh viên HCMUS. Luôn trả về JSON."},
		{Role: "user", Content: prompt},
	}

	response, _, err := AI.ChatNonStream(ServiceClassify, messages)
	if err != nil {
		log.Printf("Lỗi gọi AI Moderation: %v", err)
		return true, "Lỗi AI, tạm thời cho qua", "Chưa phân loại" // Fail-safe: Nếu AI lỗi cho qua
	}

	var res struct {
		IsAcademic   bool    `json:"is_academic"`
		QualityScore float64 `json:"quality_score"`
		SubjectArea  string  `json:"subject_area"`
	}

	cleanJSON := CleanJSONString(response)
	if err := json.Unmarshal([]byte(cleanJSON), &res); err != nil {
		log.Printf("Lỗi Unmarshal AI Moderation: %v. Response: %s", err, response)
		return true, "Lỗi format AI, tạm thời cho qua", "Chưa phân loại"
	}

	if !res.IsAcademic {
		return false, "Tài liệu không mang tính học thuật/chuyên môn", ""
	}
	if res.QualityScore < 4 {
		return false, fmt.Sprintf("Chất lượng nội dung quá thấp (Score: %.1f)", res.QualityScore), ""
	}

	return true, "", res.SubjectArea
}

// SaveRejectedHash lưu hash vào DB để chặn sau này
func SaveRejectedHash(ctx context.Context, hash string, reason string) {
	_, err := config.DB.Exec(ctx, "INSERT INTO rejected_hashes (file_hash, reason) VALUES ($1, $2) ON CONFLICT DO NOTHING", hash, reason)
	if err != nil {
		log.Printf("Không thể lưu rejected hash: %v", err)
	}
}

// UpdateDocProgress lưu trạng thái và phần trăm tiến độ vào Redis
func UpdateDocProgress(docID string, status string, progress int) {
	key := fmt.Sprintf("doc_progress:%s", docID)
	val := map[string]interface{}{
		"status":   status,
		"progress": progress,
	}
	data, _ := json.Marshal(val)
	config.RedisClient.Set(config.Ctx, key, data, 0)
}
