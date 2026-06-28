package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type stubTool struct {
	name     string
	category ToolCategory
}

func (s *stubTool) Name() string                       { return s.name }
func (s *stubTool) Description() string                { return "stub tool for tests" }
func (s *stubTool) Category() ToolCategory              { return s.category }
func (s *stubTool) Parameters() map[string]interface{} { return map[string]interface{}{"type": "object"} }
func (s *stubTool) Timeout() time.Duration              { return time.Second }
func (s *stubTool) TierLimit() map[string]int           { return nil }
func (s *stubTool) RequiresConfirmation() bool          { return false }
func (s *stubTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	return ToolResult{TextOutput: "ok"}, nil
}

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	tool := &stubTool{name: "stub_a", category: CategoryOrchestration}

	if err := r.Register(tool); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}

	got, ok := r.Get("stub_a")
	if !ok {
		t.Fatalf("Get() ok = false, want true")
	}
	if got.Name() != "stub_a" {
		t.Fatalf("Get() returned tool with name %q, want %q", got.Name(), "stub_a")
	}

	if _, ok := r.Get("missing"); ok {
		t.Fatalf("Get(\"missing\") ok = true, want false")
	}
}

func TestRegistryDuplicateNameRejected(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stubTool{name: "dup", category: CategoryOrchestration}); err != nil {
		t.Fatalf("first Register() unexpected error: %v", err)
	}
	if err := r.Register(&stubTool{name: "dup", category: CategoryOrchestration}); err == nil {
		t.Fatalf("second Register() with duplicate name: want error, got nil")
	}
}

func TestRegistryListByCategory(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&stubTool{name: "a", category: CategoryInformationRetrieval})
	_ = r.Register(&stubTool{name: "b", category: CategoryInformationRetrieval})
	_ = r.Register(&stubTool{name: "c", category: CategoryComputationExecution})

	irTools := r.ListByCategory(CategoryInformationRetrieval)
	if len(irTools) != 2 {
		t.Fatalf("ListByCategory(InformationRetrieval) len = %d, want 2", len(irTools))
	}

	ceTools := r.ListByCategory(CategoryComputationExecution)
	if len(ceTools) != 1 {
		t.Fatalf("ListByCategory(ComputationExecution) len = %d, want 1", len(ceTools))
	}
}

func TestRegistryAll(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&stubTool{name: "a", category: CategoryInformationRetrieval})
	_ = r.Register(&stubTool{name: "b", category: CategoryComputationExecution})

	all := r.All()
	if len(all) != 2 {
		t.Fatalf("All() len = %d, want 2", len(all))
	}
}
