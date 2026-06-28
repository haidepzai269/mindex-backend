package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mindex-backend/utils"
)

// stepTool is a stub tool used only to drive the dispatcher loop in
// orchestration tests - it just echoes which step it was.
type stepTool struct{ name string }

func (s *stepTool) Name() string                       { return s.name }
func (s *stepTool) Description() string                { return "stub step tool" }
func (s *stepTool) Category() ToolCategory              { return CategoryOrchestration }
func (s *stepTool) Parameters() map[string]interface{} { return map[string]interface{}{"type": "object"} }
func (s *stepTool) Timeout() time.Duration              { return time.Second }
func (s *stepTool) TierLimit() map[string]int           { return nil }
func (s *stepTool) RequiresConfirmation() bool          { return false }
func (s *stepTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	return ToolResult{TextOutput: "ran " + s.name}, nil
}

// failingStepTool always errors, used to prove a mid-chain tool failure
// doesn't stop the rest of the chain (US10 AC2: partial success reporting).
type failingStepTool struct{ name string }

func (f *failingStepTool) Name() string                       { return f.name }
func (f *failingStepTool) Description() string                { return "stub failing step tool" }
func (f *failingStepTool) Category() ToolCategory              { return CategoryOrchestration }
func (f *failingStepTool) Parameters() map[string]interface{} { return map[string]interface{}{"type": "object"} }
func (f *failingStepTool) Timeout() time.Duration              { return time.Second }
func (f *failingStepTool) TierLimit() map[string]int           { return nil }
func (f *failingStepTool) RequiresConfirmation() bool          { return false }
func (f *failingStepTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	return ToolResult{}, errStepFailed
}

var errStepFailed = errStepFailedType{}

type errStepFailedType struct{}

func (errStepFailedType) Error() string { return "simulated step failure" }

// TestDispatcherRunChainsMultipleToolRounds drives a 3-round conversation
// (web_search -> web_fetch equivalent -> final answer) entirely through
// stub tools and a scripted chatFunc, proving the loop correctly chains
// multiple sequential tool rounds (US10: "Tìm 3 bài viết... rồi tóm tắt").
func TestDispatcherRunChainsMultipleToolRounds(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(&stepTool{name: "step_a"})
	_ = reg.Register(&stepTool{name: "step_b"})
	d := NewDispatcher(reg)

	round := 0
	d.chatFunc = func(service utils.ServiceType, messages []utils.ChatMessage, schemas []utils.ToolSchema) (string, []utils.ToolCallRequest, utils.ProviderType, error) {
		round++
		switch round {
		case 1:
			return "", []utils.ToolCallRequest{{ID: "1", Name: "step_a", Arguments: "{}"}}, "stub", nil
		case 2:
			return "", []utils.ToolCallRequest{{ID: "2", Name: "step_b", Arguments: "{}"}}, "stub", nil
		default:
			return "Tổng hợp xong cả hai bước.", nil, "stub", nil
		}
	}

	result, err := d.Run(context.Background(), ToolExecutionContext{}, []utils.ChatMessage{{Role: "user", Content: "test"}}, 10)
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if result.FinalAnswer != "Tổng hợp xong cả hai bước." {
		t.Fatalf("Run().FinalAnswer = %q, want the round-3 answer", result.FinalAnswer)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("Run().ToolCalls len = %d, want 2 (one per chained round)", len(result.ToolCalls))
	}
	if result.ToolCalls[0].ToolName != "step_a" || result.ToolCalls[1].ToolName != "step_b" {
		t.Fatalf("Run().ToolCalls = %+v, want step_a then step_b in order", result.ToolCalls)
	}
	if round != 3 {
		t.Fatalf("chatFunc was called %d times, want exactly 3 (2 tool rounds + 1 final)", round)
	}
}

// TestDispatcherRunReportsPartialSuccessOnMidChainFailure proves a failing
// tool mid-chain doesn't abort the rest of the chain - the failure is
// recorded and the loop continues (US10 AC2, FR-005).
func TestDispatcherRunReportsPartialSuccessOnMidChainFailure(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(&failingStepTool{name: "step_fail"})
	_ = reg.Register(&stepTool{name: "step_ok"})
	d := NewDispatcher(reg)

	round := 0
	d.chatFunc = func(service utils.ServiceType, messages []utils.ChatMessage, schemas []utils.ToolSchema) (string, []utils.ToolCallRequest, utils.ProviderType, error) {
		round++
		switch round {
		case 1:
			return "", []utils.ToolCallRequest{
				{ID: "1", Name: "step_fail", Arguments: "{}"},
				{ID: "2", Name: "step_ok", Arguments: "{}"},
			}, "stub", nil
		default:
			return "Một bước lỗi, một bước thành công.", nil, "stub", nil
		}
	}

	result, err := d.Run(context.Background(), ToolExecutionContext{}, []utils.ChatMessage{{Role: "user", Content: "test"}}, 10)
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("Run().ToolCalls len = %d, want 2 (both attempted despite one failing)", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Status != "error" {
		t.Fatalf("Run().ToolCalls[0].Status = %q, want %q", result.ToolCalls[0].Status, "error")
	}
	if result.ToolCalls[1].Status != "success" {
		t.Fatalf("Run().ToolCalls[1].Status = %q, want %q", result.ToolCalls[1].Status, "success")
	}
	if !strings.Contains(result.FinalAnswer, "thành công") {
		t.Fatalf("Run().FinalAnswer = %q, want it to still report the successful step", result.FinalAnswer)
	}
}

// TestDispatcherRunStopsAtMaxDepthWithPartialResults proves the hard
// maxDepth cutoff (FR-015, Edge Case: vòng lặp tool) returns whatever was
// accomplished instead of an error (US10 AC3's ">10 steps" framing).
func TestDispatcherRunStopsAtMaxDepthWithPartialResults(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(&stepTool{name: "loop_step"})
	d := NewDispatcher(reg)

	calls := 0
	d.chatFunc = func(service utils.ServiceType, messages []utils.ChatMessage, schemas []utils.ToolSchema) (string, []utils.ToolCallRequest, utils.ProviderType, error) {
		calls++
		// Always asks for another tool call - simulates a runaway/looping plan.
		return "", []utils.ToolCallRequest{{ID: "x", Name: "loop_step", Arguments: "{}"}}, "stub", nil
	}

	const cap = 4
	result, err := d.Run(context.Background(), ToolExecutionContext{}, []utils.ChatMessage{{Role: "user", Content: "test"}}, cap)
	if err != nil {
		t.Fatalf("Run() at maxDepth cutoff: want nil error (partial success, not failure), got %v", err)
	}
	if calls != cap {
		t.Fatalf("chatFunc was called %d times, want exactly maxDepth=%d", calls, cap)
	}
	if len(result.ToolCalls) != cap {
		t.Fatalf("Run().ToolCalls len = %d, want %d (one per round before the cutoff)", len(result.ToolCalls), cap)
	}
}
