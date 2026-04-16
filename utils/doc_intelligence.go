package utils

import (
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
)

type DocIntelligence struct {
	DocID       string           `json:"doc_id"`
	MainTopic   string           `json:"main_topic"`
	Thesis      string           `json:"thesis"`
	DocType     string           `json:"doc_type"`
	Outline     interface{}      `json:"outline"`
	KeyConcepts interface{}      `json:"key_concepts"`
}

const docIntelPrompt = `
Phân tích tài liệu sau và trả về JSON (không thêm bất kỳ văn bản nào khác).
JSON schema yêu cầu:
{
  "main_topic": "chủ đề chính 1 câu",
  "thesis": "luận điểm trung tâm hoặc mục tiêu của tài liệu",
  "doc_type": "slide|book|paper|report|manual",
  "outline": [{"title": "Chương 1", "children": [{"title": "Mục 1.1"}]}],
  "key_concepts": [{"name": "khái niệm", "definition": "...", "related_to": ["..."]}]
}

Nội dung tài liệu:
%s
`

// AnalyzeDocument gọi LLM để tạo bản đồ tri thức cho tài liệu
func AnalyzeDocument(docID string, fullText string) (*DocIntelligence, error) {
	// Giới hạn input cho LLM (ví dụ 15k ký tự đầu)
	input := fullText
	runes := []rune(fullText)
	if len(runes) > 15000 {
		input = string(runes[:15000])
	}

	messages := []ChatMessage{
		{Role: "system", Content: "Bạn là chuyên gia phân tích tài liệu chuyên sâu."},
		{Role: "user", Content: fmt.Sprintf(docIntelPrompt, input)},
	}

	// Sử dụng NineRouter hoặc Gemini Flash cho bước này (ServiceSummary thường dùng model nhanh/rẻ)
	answer, usedProvider, err := AI.ChatNonStream(ServiceSummary, messages)
	if err != nil {
		return nil, fmt.Errorf("AI analysis failed: %v", err)
	}

	log.Printf("🧠 [DocIntelligence] Analysis successful via %s for Doc %s", usedProvider, docID)

	var intel DocIntelligence
	if err := json.Unmarshal([]byte(answer), &intel); err != nil {
		// Thử tìm JSON trong chuỗi nếu LLM trả về thừa văn bản
		cleaned := CleanJSONString(answer)
		if err := json.Unmarshal([]byte(cleaned), &intel); err != nil {
			return nil, fmt.Errorf("failed to parse AI JSON: %v", err)
		}
	}

	intel.DocID = docID

	// Lưu vào DB
	outlineBytes, _ := json.Marshal(intel.Outline)
	conceptsBytes, _ := json.Marshal(intel.KeyConcepts)

	_, err = config.DB.Exec(config.Ctx, `
		INSERT INTO document_intelligence (doc_id, main_topic, thesis, doc_type, outline, key_concepts)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (doc_id) DO UPDATE SET
			main_topic = EXCLUDED.main_topic,
			thesis = EXCLUDED.thesis,
			doc_type = EXCLUDED.doc_type,
			outline = EXCLUDED.outline,
			key_concepts = EXCLUDED.key_concepts`,
		docID, intel.MainTopic, intel.Thesis, intel.DocType, outlineBytes, conceptsBytes,
	)

	return &intel, err
}
