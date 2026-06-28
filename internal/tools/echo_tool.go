package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// EchoTool is a trivial demo tool: it returns its input text unchanged.
// Its only purpose is to prove the extensibility contract from US3 - it is
// implemented and registered without any change to registry.go,
// dispatcher.go, or chat.go.
type EchoTool struct{}

func NewEchoTool() *EchoTool { return &EchoTool{} }

func (t *EchoTool) Name() string                       { return "echo" }
func (t *EchoTool) Description() string                { return "Trả lại đúng văn bản đầu vào. Dùng để demo/test tool framework." }
func (t *EchoTool) Category() ToolCategory              { return CategoryOrchestration }
func (t *EchoTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Văn bản cần trả lại nguyên văn",
			},
		},
		"required": []string{"text"},
	}
}
func (t *EchoTool) Timeout() time.Duration     { return time.Second }
func (t *EchoTool) TierLimit() map[string]int  { return nil }
func (t *EchoTool) RequiresConfirmation() bool { return false }

func (t *EchoTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{}, fmt.Errorf("echo: invalid input: %v", err)
	}
	return ToolResult{TextOutput: in.Text}, nil
}
