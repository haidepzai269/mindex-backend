package tools

import (
	"context"
	"encoding/json"
	"testing"

	"mindex-backend/config"
	"mindex-backend/utils"
)

// TestWebSearchToolMatchesDirectCallWhenDisabled is the zero-regression
// smoke test from SC-005: with web search disabled (the test-environment
// default, since config.Env is loaded with no .env file), both the direct
// utils.SearchWeb call and the WebSearchTool adapter must fail the exact
// same way - proving the adapter adds no new behavior of its own.
func TestWebSearchToolMatchesDirectCallWhenDisabled(t *testing.T) {
	if config.Env.WebSearchEnabled {
		t.Skip("WebSearchEnabled=true in this environment; skipping disabled-path smoke test")
	}

	plan := utils.WebSearchPlan{UseWebSearch: true, Query: "tỷ giá USD hôm nay"}
	_, directErr := utils.SearchWeb(context.Background(), plan)
	if directErr == nil {
		t.Fatalf("direct SearchWeb() expected error when disabled, got nil")
	}

	tool := NewWebSearchTool()
	input, _ := json.Marshal(map[string]string{"query": plan.Query})
	_, toolErr := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if toolErr == nil {
		t.Fatalf("WebSearchTool.Execute() expected error when disabled, got nil")
	}

	if directErr.Error() != "web search is disabled" {
		t.Fatalf("direct call error = %q, want %q", directErr.Error(), "web search is disabled")
	}
}

func TestWebSearchToolInvalidInput(t *testing.T) {
	tool := NewWebSearchTool()
	_, err := tool.Execute(context.Background(), ToolExecutionContext{}, json.RawMessage(`not-json`))
	if err == nil {
		t.Fatalf("Execute() with invalid JSON input: want error, got nil")
	}
}
