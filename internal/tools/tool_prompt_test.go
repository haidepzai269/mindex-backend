package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type promptStubTool struct {
	name string
	desc string
}

func (s *promptStubTool) Name() string                        { return s.name }
func (s *promptStubTool) Description() string                 { return s.desc }
func (s *promptStubTool) Category() ToolCategory              { return CategoryInformationRetrieval }
func (s *promptStubTool) Parameters() map[string]interface{}  { return nil }
func (s *promptStubTool) Timeout() time.Duration              { return 0 }
func (s *promptStubTool) TierLimit() map[string]int           { return nil }
func (s *promptStubTool) RequiresConfirmation() bool          { return false }
func (s *promptStubTool) Execute(_ context.Context, _ ToolExecutionContext, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{}, nil
}

func TestBuildToolUsagePromptEmpty(t *testing.T) {
	r := NewRegistry()
	result := BuildToolUsagePrompt(r)
	if result != "" {
		t.Errorf("expected empty string for empty registry, got %q", result)
	}
}

func TestBuildToolUsagePromptContainsTools(t *testing.T) {
	r := NewRegistry()
	r.Register(&promptStubTool{name: "weather", desc: "Lấy thời tiết thành phố"})
	r.Register(&promptStubTool{name: "crypto", desc: "Lấy giá crypto"})

	result := BuildToolUsagePrompt(r)

	if !strings.Contains(result, "weather") {
		t.Error("prompt should contain tool name 'weather'")
	}
	if !strings.Contains(result, "crypto") {
		t.Error("prompt should contain tool name 'crypto'")
	}
	if !strings.Contains(result, "Lấy thời tiết thành phố") {
		t.Error("prompt should contain tool description")
	}
	if !strings.Contains(result, "[AVAILABLE TOOLS") {
		t.Error("prompt should contain header")
	}
}

func TestBuildToolUsagePromptContainsRules(t *testing.T) {
	r := NewRegistry()
	r.Register(&promptStubTool{name: "test_tool", desc: "test"})

	result := BuildToolUsagePrompt(r)

	if !strings.Contains(result, "KHÔNG BAO GIỜ tự bịa") {
		t.Error("prompt should contain rule about not fabricating data")
	}
}
