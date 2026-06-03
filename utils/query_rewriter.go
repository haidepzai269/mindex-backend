package utils

import (
	"fmt"
	"log"
	"strings"
)

// ambiguousReferenceTokens là các đại từ/từ chỉ định mà nếu xuất hiện trong câu hỏi
// thì cần dùng history để làm rõ. Nếu không có, query đã tự đủ nghĩa.
var ambiguousReferenceTokens = []string{
	// Tiếng Việt
	"nó", "cái đó", "chúng", "họ", "điều này", "điều đó", "cái này", "cái kia",
	"phương pháp đó", "khái niệm đó", "khái niệm trên", "phương pháp trên",
	"vấn đề đó", "vấn đề này", "ý đó", "ý trên", "bước đó", "bước này",
	"ví dụ đó", "ví dụ trên", "loại đó", "loại này", "mô hình đó", "mô hình trên",
	"tiếp theo", "còn về", "còn cái", "thế còn", "vậy còn", "còn gì",
	// English
	"it ", "they ", "them ", "that ", "this ", "those ", "these ",
	"the above", "the previous", "mentioned above", "as above",
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
