package utils

import (
	"regexp"
	"strings"
)

var (
	// 1. Số trang: "Trang 1", "Page 1", "1/50", "(1)", "[1]" - Chỉ khớp khi đứng RIÊNG BIỆT trên 1 dòng
	pageNumberRegex = regexp.MustCompile(`(?i)^\s*(trang|page)\s*\d+\s*$|^\s*\d+\s*/\s*\d+\s*$|^\s*[\(\[]\d+[\)\]]\s*$`)
	
	// 2. Ký tự điều khiển PDF: \f (Form Feed - dùng để ngắt trang)
	formFeedRegex = regexp.MustCompile(`\f`)
	
	// 3. Khoảng trắng dư thừa
	multipleNewlinesRegex = regexp.MustCompile(`\n{3,}`)
	multipleSpacesRegex   = regexp.MustCompile(` {2,}`)

	// 4. Các ký tự rác phổ biến từ PDF extractor
	garbageCharsRegex = regexp.MustCompile(`(?m)[\x00-\x08\x0B\x0C\x0E-\x1F]`) 
)

// CleanTextLocal là bộ lọc text cục bộ thay thế cho Groq LLM (SYS-001)
// Giúp làm sạch 90% rác từ PDF mà không tốn xu nào.
func CleanTextLocal(text string) string {
	// Bước 0: Đảm bảo UTF-8 hợp lệ để tránh lỗi Protobuf/JSON
	text = strings.ToValidUTF8(text, "")

	// Bước 1: Xóa ký tự điều khiển và rác hex
	text = formFeedRegex.ReplaceAllString(text, "\n")
	text = garbageCharsRegex.ReplaceAllString(text, "")

	// Bước 2: Xử lý theo từng dòng
	lines := strings.Split(text, "\n")
	var cleanedLines []string

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		
		// Bỏ qua dòng trống
		if trimmedLine == "" {
			continue
		}

		// Kiểm tra nếu dòng là số trang đơn thuần
		if pageNumberRegex.MatchString(trimmedLine) && len(trimmedLine) < 15 {
			continue
		}

		// Chuẩn hóa khoảng trắng trong dòng
		trimmedLine = multipleSpacesRegex.ReplaceAllString(trimmedLine, " ")
		
		cleanedLines = append(cleanedLines, trimmedLine)
	}

	// Bước 3: Nối lại và chuẩn hóa xuống dòng
	result := strings.Join(cleanedLines, "\n")
	result = multipleNewlinesRegex.ReplaceAllString(result, "\n\n")

	return strings.TrimSpace(result)
}

// RemoveVietnameseSigns loại bỏ dấu tiếng Việt để phục vụ tìm kiếm fuzzy/keyword
func RemoveVietnameseSigns(str string) string {
	str = strings.ToLower(str)
	var signs = map[string]string{
		"a": "áàảãạâấầẩẫậăắằẳẵặ",
		"e": "éèẻẽẹêếềểễệ",
		"i": "íìỉĩị",
		"o": "óòỏõọôốồổỗộơớờởỡợ",
		"u": "úùủũụưứừửữự",
		"y": "ýỳỷỹỵ",
		"d": "đ",
	}
	for replace, regex := range signs {
		for _, char := range regex {
			str = strings.ReplaceAll(str, string(char), replace)
		}
	}
	return str
}
