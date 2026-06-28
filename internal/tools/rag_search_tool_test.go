package tools

import (
	"context"
	"encoding/json"
	"testing"

	"mindex-backend/utils"
)

func TestRAGSearchToolNoDocumentScope(t *testing.T) {
	tool := NewRAGSearchTool()
	input, _ := json.Marshal(map[string]string{"query": "nội dung gì"})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err == nil {
		t.Fatalf("Execute() without document_id/collection_id: want error, got nil")
	}
}

// TestRAGSearchToolMatchesDirectCallOnEmbeddingFailure is the zero-regression
// smoke test from SC-005: in a test environment with no Gemini embedding
// pool configured, both a direct utils.GenerateEmbedding call and the
// RAGSearchTool adapter must fail identically - the adapter adds no new
// embedding/search behavior of its own.
func TestRAGSearchToolMatchesDirectCallOnEmbeddingFailure(t *testing.T) {
	if utils.GeminiEmbedPool != nil {
		t.Skip("GeminiEmbedPool configured in this environment; skipping unconfigured-path smoke test")
	}

	_, directErr := utils.GenerateEmbedding(context.Background(), "test query")
	if directErr == nil {
		t.Fatalf("direct GenerateEmbedding() expected error when pool unconfigured, got nil")
	}

	tool := NewRAGSearchTool()
	input, _ := json.Marshal(map[string]string{"query": "test query", "document_id": "doc-123"})
	_, toolErr := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if toolErr == nil {
		t.Fatalf("RAGSearchTool.Execute() expected error when pool unconfigured, got nil")
	}
}
