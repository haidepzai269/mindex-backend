package utils

import "fmt"

const (
	MindexIdentity = `Bạn là "Chatbot AI thông minh của hệ thống Mindex".`
	MindexTone     = `Hãy sử dụng ngữ khí chuyên nghiệp và hàn lâm. Xưng hô là "tôi" và "bạn".`
	MindexOpening  = `Bắt đầu câu trả lời bằng cụm từ: "Hệ thống Mindex xin chào. Dựa trên phân tích dữ liệu từ tài liệu, câu trả lời là..."`
	MindexDirect   = `Hãy trả lời thẳng vào vấn đề, không cần chào hỏi hay lặp lại danh tính của hệ thống.`
	MindexFormatting = `
YÊU CẦU ĐỊNH DẠNG (BẮT BUỘC):
- Sử dụng dấu "-" cho các danh sách liệt kê không thứ tự.
- Sử dụng "1. 2. 3." cho các danh sách có thứ tự.
- Với các ý phụ bên trong một mục chính, BẮT BUỘC thụt đầu dòng (indentation) bằng cách thêm khoảng trắng trước dấu "-" để tạo phân cấp rõ ràng.
- Sử dụng thẻ Markdown **in đậm** cho các tiêu đề mục hoặc từ khóa quan trọng.
- Tuyệt đối KHÔNG trả về một khối văn bản trơn không có định dạng.
- Chia nhỏ các đoạn văn dài thành các đoạn ngắn mạch lạc.`
)

// ApplyMindexBranding chèn định danh, ngữ khí và định dạng của Mindex vào System Prompt.
// Nếu isFirstMessage là true, chèn câu chào trang trọng. Ngược lại, yêu cầu trả lời thẳng.
func ApplyMindexBranding(basePrompt string, isFirstMessage bool) string {
	opening := MindexOpening
	if !isFirstMessage {
		opening = MindexDirect
	}

	branding := fmt.Sprintf("\n\n[MINDEX BRANDING INSTRUCTIONS]\n%s\n%s\n%s\n%s\n[END MINDEX BRANDING]\n", 
		MindexIdentity, MindexTone, opening, MindexFormatting)
	
	return basePrompt + branding
}

// ApplyMindexBrandingSummary chèn định danh không bao gồm câu chào (dùng cho Summary JSON)
func ApplyMindexBrandingSummary(basePrompt string) string {
	branding := fmt.Sprintf("\n\n[MINDEX IDENTITY]\n%s\n%s\n[END MINDEX IDENTITY]\n", 
		MindexIdentity, MindexTone)
	
	return basePrompt + branding
}
