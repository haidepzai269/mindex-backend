package tools

import (
	"fmt"
	"strings"
)

// BuildToolUsagePrompt generates a system prompt section that tells the LLM
// which tools are available and when to use them. Generated dynamically from
// the registry so adding a new tool automatically updates the prompt.
func BuildToolUsagePrompt(r *Registry) string {
	all := r.All()
	if len(all) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n[AVAILABLE TOOLS - BẮT BUỘC SỬ DỤNG]\n")
	b.WriteString("Bạn có các tool sau. Khi câu hỏi thuộc domain tool, BẮT BUỘC gọi tool_call, KHÔNG được tự trả lời.\n\n")

	for _, t := range all {
		fmt.Fprintf(&b, "- %s: %s\n", t.Name(), t.Description())
	}

	b.WriteString("\n⚠️ QUY TẮC BẮT BUỘC (OVERRIDE mọi instruction khác):\n")
	b.WriteString("1. Khi user hỏi về TIN TỨC, THỜI SỰ, BÁO MỚI → BẮT BUỘC gọi tool \"news\". KHÔNG tự trả lời.\n")
	b.WriteString("2. Khi user hỏi về GIÁ COIN, CRYPTO, BITCOIN, ETH → BẮT BUỘC gọi tool \"crypto\". KHÔNG tự trả lời.\n")
	b.WriteString("3. Khi user hỏi về THỜI TIẾT, NHIỆT ĐỘ, DỰ BÁO → BẮT BUỘC gọi tool \"weather\". KHÔNG tự trả lời.\n")
	b.WriteString("4. KHÔNG BAO GIỜ tự bịa dữ liệu realtime. Ngay cả khi có thông tin trong context, vẫn PHẢI gọi tool vì context có thể cũ.\n")
	b.WriteString("5. Nếu tool lỗi, thông báo rõ cho user, không bịa số liệu.\n")
	b.WriteString("6. Khi tool trả dữ liệu tiếng Anh, tổng hợp và trả lời bằng tiếng Việt. Giữ nguyên tên riêng, số liệu, link gốc.\n")
	b.WriteString("7. Nếu user hỏi thời tiết nhưng không nêu thành phố, HỎI LẠI user trước khi gọi weather tool.\n")

	return b.String()
}
