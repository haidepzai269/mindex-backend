package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"regexp"
	"strings"
	"time"
)

type ModerationResult struct {
	Passed      bool   `json:"passed"`
	Reason      string `json:"reason"`
	Tier        int    `json:"tier"`
	SubjectArea string `json:"subject_area"`
}

var (
	spamURLRegex = regexp.MustCompile(`(bit\.ly|t\.co|tinyurl\.com|cutt\.ly|shurte\.st|goo\.gl|ow\.ly|is\.gd|buff\.ly|bit\.do)`)
	phoneRegex   = regexp.MustCompile(`(0|\+84)(\s|\.)?((3[2-9])|(5[689])|(7[06-9])|(8[1-689])|(9[0-46-9]))(\d)(\s|\.)?(\d{3})(\s|\.)?(\d{3})`)

	redFlagKeywords = []string{
		"gia chi", "lien he ngay", "uu dai hom nay", "mua ngay", "gia re nhat",
		"click here", "free download", "tai ngay", "truc tiep bong da", "keo nha cai",
		"xo so", "soi cau", "kiem tien online", "tuyen dung gap", "lam viec tai nha",
		"nhan qua", "trung thuong", "khuyen mai cuc lon", "duy nhat hom nay",
	}
)

// T1RuleBased performs deterministic moderation that must not fail open.
func T1RuleBased(ctx context.Context, fileHash string, tokenCount int, charCount int, rawText string) (bool, string) {
	var exists bool
	err := config.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM rejected_hashes WHERE file_hash = $1)", fileHash).Scan(&exists)
	if err == nil && exists {
		return false, "File da bi tu choi truoc do"
	}

	if tokenCount < 50 {
		return false, fmt.Sprintf("Tai lieu qua it noi dung (co %d tokens, yeu cau toi thieu 50)", tokenCount)
	}
	if charCount < 200 {
		return false, fmt.Sprintf("Tai lieu khong du do dai hoac khong doc duoc (co %d ky tu, yeu cau toi thieu 200)", charCount)
	}

	if spamURLRegex.MatchString(strings.ToLower(rawText)) {
		return false, "Phat hien link spam hoac rut gon trai phep"
	}

	if matches := phoneRegex.FindAllString(rawText, -1); len(matches) > 3 {
		return false, "Phat hien qua nhieu so dien thoai"
	}

	return true, ""
}

// T2KeywordCheck checks obvious spam language before spending AI quota.
func T2KeywordCheck(rawText string) (bool, string) {
	words := strings.Fields(rawText)
	limit := 200
	if len(words) < limit {
		limit = len(words)
	}
	firstContent := strings.ToLower(strings.Join(words[:limit], " "))

	foundCount := 0
	for _, kw := range redFlagKeywords {
		if strings.Contains(firstContent, strings.ToLower(kw)) {
			foundCount++
		}
		if foundCount >= 2 {
			return false, fmt.Sprintf("Phat hien tu khoa vi pham: %s", kw)
		}
	}

	return true, ""
}

// T3AICheck returns an error for transient AI/parsing failures so upload jobs can retry.
func T3AICheck(rawText string) (bool, string, string, error) {
	words := strings.Fields(rawText)
	limit := 500
	if len(words) < limit {
		limit = len(words)
	}
	sampleText := strings.Join(words[:limit], " ")

	cleanSample := regexp.MustCompile(`[\r\n\t]+`).ReplaceAllString(sampleText, " ")
	cleanSample = regexp.MustCompile(`[^\p{L}\p{N}\p{P}\s]+`).ReplaceAllString(cleanSample, "")
	runes := []rune(cleanSample)
	if len(runes) > 2000 {
		cleanSample = string(runes[:2000])
	}

	prompt := fmt.Sprintf(`Phan tich 500 tu dau tien cua tai lieu nay va tra ve JSON duy nhat:
{
  "is_academic": boolean,
  "quality_score": number (1-10),
  "subject_area": string
}
Luu y:
- Hoc thuat (is_academic = true) bao gom: giao trinh, bao cao khoa hoc, tieu luan, huong dan ky thuat.
- Tu choi neu la van ban rac, quang cao, hoac noi dung khong mang tinh giao duc.

Noi dung: %s`, cleanSample)

	messages := []ChatMessage{
		{Role: "system", Content: "Ban la mot AI chuyen phan loai tai lieu hoc thuat. Luon tra ve JSON."},
		{Role: "user", Content: prompt},
	}

	response, _, err := AI.ChatNonStream(ServiceClassify, messages)
	if err != nil {
		log.Printf("AI moderation call failed: %v", err)
		return false, "Loi AI moderation", "", err
	}

	var res struct {
		IsAcademic   bool    `json:"is_academic"`
		QualityScore float64 `json:"quality_score"`
		SubjectArea  string  `json:"subject_area"`
	}

	cleanJSON := CleanJSONString(response)
	if err := json.Unmarshal([]byte(cleanJSON), &res); err != nil {
		log.Printf("AI moderation JSON parse failed: %v. Response: %s", err, response)
		return false, "Loi format AI moderation", "", err
	}

	if !res.IsAcademic {
		return false, "Tai lieu khong mang tinh hoc thuat/chuyen mon", "", nil
	}
	if res.QualityScore < 4 {
		return false, fmt.Sprintf("Chat luong noi dung qua thap (Score: %.1f)", res.QualityScore), "", nil
	}

	return true, "", res.SubjectArea, nil
}

func SaveRejectedHash(ctx context.Context, hash string, reason string) {
	_, err := config.DB.Exec(ctx, "INSERT INTO rejected_hashes (file_hash, reason) VALUES ($1, $2) ON CONFLICT DO NOTHING", hash, reason)
	if err != nil {
		log.Printf("Could not save rejected hash: %v", err)
	}
}

func UpdateDocProgress(docID string, status string, progress int) {
	UpdateDocProgressDetail(docID, status, progress, "", "")
}

func UpdateDocProgressDetail(docID string, status string, progress int, message string, errorCode string) {
	if config.RedisClient == nil {
		return
	}

	key := fmt.Sprintf("doc_progress:%s", docID)
	val := map[string]interface{}{
		"status":   status,
		"progress": progress,
	}
	if message != "" {
		val["message"] = message
	}
	if errorCode != "" {
		val["error_code"] = errorCode
	}

	data, _ := json.Marshal(val)
	config.RedisClient.Set(config.Ctx, key, data, 24*time.Hour)
}
