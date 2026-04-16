package utils

import (
	"fmt"
	"log"
)

// EnrichChunk thêm ngữ cảnh cho một đoạn văn dựa trên nội dung toàn tài liệu
func EnrichChunk(chunkContent string, docSummary string) (string, error) {
	// Prompt tối ưu theo kỹ thuật của Anthropic
	prompt := fmt.Sprintf(`
Tài liệu của chúng ta có luân điểm/chủ đề sau: %s

Đoạn văn dưới đây là một phần rải rác trong tài liệu đó. Hãy viết 1-2 câu ngắn đặt đoạn văn này vào ngữ cảnh của toàn tài liệu để AI có thể hiểu rõ hơn khi tìm kiếm. 
Lưu ý: Chỉ trả về phần ngữ cảnh bổ sung, không lặp lại nội dung gốc, không giải thích thêm.

Đoạn văn cần xử lý:
%s
`, docSummary, chunkContent)

	messages := []ChatMessage{
		{Role: "system", Content: "Bạn là chuyên gia làm giàu ngữ cảnh cho hệ thống tìm kiếm RAG."},
		{Role: "user", Content: prompt},
	}

	// Sử dụng NineRouter (mặc định model Mindex/Llama 8B) theo yêu cầu người dùng
	// Đây là bước quan trọng cần độ trễ thấp và chi phí rẻ
	answer, usedProvider, err := AI.ChatNonStream(ServiceSearch, messages)
	if err != nil {
		return "", fmt.Errorf("chunk enrichment failed: %v", err)
	}

	log.Printf("✨ [Enricher] Context added via %s for a chunk", usedProvider)

	// Trả về ngữ cảnh + nội dung gốc
	// Để cấu trúc rõ ràng cho Vector Embedding hiểu
	return fmt.Sprintf("Context: %s\n\nContent: %s", answer, chunkContent), nil
}
