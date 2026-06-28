package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"mindex-backend/utils"
)

// WebSearchTool adapts the existing multi-provider web search
// (utils.SearchWeb) to the Tool interface, per FR-008: zero lines of
// utils/web_search.go change, this file only translates input/output.
type WebSearchTool struct{}

func NewWebSearchTool() *WebSearchTool { return &WebSearchTool{} }

func (t *WebSearchTool) Name() string { return "web_search" }
func (t *WebSearchTool) Description() string {
	return "Tìm kiếm thông tin trên web (thời tiết, tỷ giá, tin tức, kiến thức ngoài tài liệu). Trả về danh sách kết quả có nguồn."
}
func (t *WebSearchTool) Category() ToolCategory { return CategoryInformationRetrieval }
func (t *WebSearchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Truy vấn tìm kiếm, viết lại ngắn gọn từ câu hỏi user",
			},
			"content_type": map[string]interface{}{
				"type":        "string",
				"description": "Loại nội dung cần tìm, ví dụ \"news\", \"general\" (tùy chọn)",
			},
		},
		"required": []string{"query"},
	}
}
func (t *WebSearchTool) Timeout() time.Duration { return 25 * time.Second }
func (t *WebSearchTool) TierLimit() map[string]int {
	return map[string]int{"FREE": 6, "PRO": 20, "ULTRA": 40}
}
func (t *WebSearchTool) RequiresConfirmation() bool { return false }

type webSearchInput struct {
	Query       string `json:"query"`
	ContentType string `json:"content_type"`
}

func (t *WebSearchTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	var in webSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{}, fmt.Errorf("web_search: invalid input: %v", err)
	}

	plan := utils.WebSearchPlan{
		UseWebSearch: true,
		Query:        in.Query,
		ContentType:  in.ContentType,
	}

	resp, err := utils.SearchWeb(ctx, plan)
	if err != nil {
		return ToolResult{}, fmt.Errorf("web_search: %v", err)
	}

	text := utils.FormatWebSearchContext(resp)
	outputJSON, _ := json.Marshal(resp)
	return ToolResult{TextOutput: text, JSONOutput: outputJSON}, nil
}
