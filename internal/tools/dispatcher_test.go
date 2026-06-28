package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mindex-backend/utils"
)

// confirmTool is a stub side-effect tool used to test the confirmation
// short-circuit (FR-009, SC-008) without depending on a real CRUD tool.
type confirmTool struct{}

func (c *confirmTool) Name() string                       { return "confirm_stub" }
func (c *confirmTool) Description() string                { return "stub" }
func (c *confirmTool) Category() ToolCategory              { return CategoryExternalActions }
func (c *confirmTool) Parameters() map[string]interface{} { return map[string]interface{}{"type": "object"} }
func (c *confirmTool) Timeout() time.Duration              { return time.Second }
func (c *confirmTool) TierLimit() map[string]int           { return nil }
func (c *confirmTool) RequiresConfirmation() bool          { return true }
func (c *confirmTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	return ToolResult{TextOutput: "should not run without confirmation"}, nil
}

// slowTool always exceeds its own timeout, to test FR-005's timeout handling.
type slowTool struct{}

func (s *slowTool) Name() string                       { return "slow_stub" }
func (s *slowTool) Description() string                { return "stub" }
func (s *slowTool) Category() ToolCategory              { return CategoryComputationExecution }
func (s *slowTool) Parameters() map[string]interface{} { return map[string]interface{}{"type": "object"} }
func (s *slowTool) Timeout() time.Duration              { return 10 * time.Millisecond }
func (s *slowTool) TierLimit() map[string]int           { return nil }
func (s *slowTool) RequiresConfirmation() bool          { return false }
func (s *slowTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	select {
	case <-time.After(200 * time.Millisecond):
		return ToolResult{TextOutput: "done"}, nil
	case <-ctx.Done():
		return ToolResult{}, ctx.Err()
	}
}

// failingTool always errors, to test FR-005's "never crash" error handling.
type failingTool struct{}

func (f *failingTool) Name() string                       { return "failing_stub" }
func (f *failingTool) Description() string                { return "stub" }
func (f *failingTool) Category() ToolCategory              { return CategoryComputationExecution }
func (f *failingTool) Parameters() map[string]interface{} { return map[string]interface{}{"type": "object"} }
func (f *failingTool) Timeout() time.Duration              { return time.Second }
func (f *failingTool) TierLimit() map[string]int           { return nil }
func (f *failingTool) RequiresConfirmation() bool          { return false }
func (f *failingTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	panic("simulated tool crash")
}

func TestDispatcherExecuteOneTimesOut(t *testing.T) {
	d := NewDispatcher(NewRegistry())
	call := utils.ToolCallRequest{ID: "1", Name: "slow_stub", Arguments: "{}"}

	rec := d.executeOne(context.Background(), ToolExecutionContext{}, &slowTool{}, call)
	if rec.Status != "timeout" {
		t.Fatalf("executeOne() status = %q, want %q", rec.Status, "timeout")
	}
}

func TestDispatcherExecuteOneRecoversFromPanic(t *testing.T) {
	d := NewDispatcher(NewRegistry())
	call := utils.ToolCallRequest{ID: "1", Name: "failing_stub", Arguments: "{}"}

	rec := d.executeOne(context.Background(), ToolExecutionContext{}, &failingTool{}, call)
	if rec.Status != "error" {
		t.Fatalf("executeOne() status = %q, want %q (tool panic must not crash the dispatcher)", rec.Status, "error")
	}
}

func TestBuildFallbackAnswerIncludesAllRecords(t *testing.T) {
	records := []ToolCallRecord{
		{ToolName: "calculator", Status: "success", Output: json.RawMessage(`{"text":"1113571"}`)},
		{ToolName: "web_search", Status: "timeout", ErrorMessage: "vượt timeout 25s"},
		{ToolName: "rag_search", Status: "error", ErrorMessage: "embedding failed"},
	}

	answer := buildFallbackAnswer(records)

	for _, want := range []string{"calculator", "1113571", "web_search", "vượt timeout 25s", "rag_search", "embedding failed"} {
		if !strings.Contains(answer, want) {
			t.Fatalf("buildFallbackAnswer() = %q, want it to contain %q", answer, want)
		}
	}
}

func TestDispatcherToolSchemasReflectRegistry(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&confirmTool{})
	d := NewDispatcher(r)

	schemas := d.toolSchemas()
	if len(schemas) != 1 || schemas[0].Name != "confirm_stub" {
		t.Fatalf("toolSchemas() = %+v, want one schema named confirm_stub", schemas)
	}
}
