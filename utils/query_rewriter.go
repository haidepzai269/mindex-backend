package utils

import (
	"fmt"
	"log"
)

// RewriteQueryWithHistory sử dụng LLM để biến câu hỏi của user thành một query độc lập dựa trên lịch sử chat (SYS-023)
func RewriteQueryWithHistory(userQuestion string, historySummary string) string {
	if historySummary == "" {
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
