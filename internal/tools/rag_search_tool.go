package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"mindex-backend/utils"
)

// RAGSearchTool adapts the existing hybrid (vector + BM25) document search
// (utils.HybridSearch) to the Tool interface, per FR-008: zero lines of
// utils/hybrid_search.go change. Lets the AI re-query the user's documents
// mid-conversation with a different query than the one used to build the
// initial chat context.
type RAGSearchTool struct{}

func NewRAGSearchTool() *RAGSearchTool { return &RAGSearchTool{} }

func (t *RAGSearchTool) Name() string { return "rag_search" }
func (t *RAGSearchTool) Description() string {
	return "Tìm kiếm trong tài liệu nội bộ user đã upload (vector + full-text). Dùng khi cần tìm lại đoạn tài liệu liên quan với truy vấn khác."
}
func (t *RAGSearchTool) Category() ToolCategory { return CategoryInformationRetrieval }
func (t *RAGSearchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Truy vấn tìm kiếm trong tài liệu",
			},
			"document_id": map[string]interface{}{
				"type":        "string",
				"description": "ID tài liệu cần tìm trong (tùy chọn, để trống nếu tìm theo collection)",
			},
			"collection_id": map[string]interface{}{
				"type":        "string",
				"description": "ID collection cần tìm trong (tùy chọn)",
			},
		},
		"required": []string{"query"},
	}
}
func (t *RAGSearchTool) Timeout() time.Duration     { return 10 * time.Second }
func (t *RAGSearchTool) TierLimit() map[string]int  { return nil }
func (t *RAGSearchTool) RequiresConfirmation() bool { return false }

type ragSearchInput struct {
	Query        string `json:"query"`
	DocumentID   string `json:"document_id"`
	CollectionID string `json:"collection_id"`
}

func (t *RAGSearchTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	var in ragSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{}, fmt.Errorf("rag_search: invalid input: %v", err)
	}
	if in.DocumentID == "" && in.CollectionID == "" {
		return ToolResult{}, fmt.Errorf("rag_search: không tìm thấy tài liệu, hãy upload tài liệu trước khi tìm kiếm")
	}

	queryVec, err := utils.GenerateEmbedding(ctx, in.Query)
	if err != nil {
		return ToolResult{}, fmt.Errorf("rag_search: embedding failed: %v", err)
	}

	const topK = 5
	chunks, _, err := utils.HybridSearch(in.DocumentID, in.CollectionID, in.Query, queryVec, topK)
	if err != nil {
		return ToolResult{}, fmt.Errorf("rag_search: %v", err)
	}

	outputJSON, _ := json.Marshal(chunks)
	return ToolResult{TextOutput: formatChunksForPrompt(chunks), JSONOutput: outputJSON}, nil
}

func formatChunksForPrompt(chunks []utils.ChunkResult) string {
	if len(chunks) == 0 {
		return ""
	}
	text := ""
	for _, c := range chunks {
		text += fmt.Sprintf("[%s, trang %d]\n%s\n\n", c.DocTitle, c.PageNumber, c.RetrievalContent)
	}
	return text
}
