package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEchoToolExecute(t *testing.T) {
	tool := NewEchoTool()
	input, _ := json.Marshal(map[string]string{"text": "hello world"})
	result, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if result.TextOutput != "hello world" {
		t.Fatalf("Execute() = %q, want %q", result.TextOutput, "hello world")
	}
}

func TestEchoToolRegistersWithoutCoreChanges(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(NewEchoTool()); err != nil {
		t.Fatalf("Register(EchoTool) unexpected error: %v", err)
	}
	got, ok := r.Get("echo")
	if !ok || got.Name() != "echo" {
		t.Fatalf("Get(\"echo\") ok=%v, want a registered echo tool", ok)
	}
}
