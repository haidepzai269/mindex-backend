package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCodeSandboxToolRejectsEmptyCode(t *testing.T) {
	tool := NewCodeSandboxTool()
	input, _ := json.Marshal(codeSandboxInput{Code: ""})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{UserID: "u1"}, input)
	if err == nil {
		t.Fatal("expected error for empty code")
	}
}

func TestCodeSandboxToolRejectsMissingAPIKey(t *testing.T) {
	tool := NewCodeSandboxTool()
	input, _ := json.Marshal(codeSandboxInput{Code: "print('hi')"})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{UserID: "u1"}, input)
	if err == nil {
		t.Fatal("expected error when E2B API key is not configured")
	}
}

func TestCodeSandboxToolMetadata(t *testing.T) {
	tool := NewCodeSandboxTool()
	if tool.Name() != "code_sandbox" {
		t.Errorf("Name() = %q, want code_sandbox", tool.Name())
	}
	if tool.Category() != CategoryComputationExecution {
		t.Errorf("Category() = %v, want computation_execution", tool.Category())
	}
	if tool.RequiresConfirmation() {
		t.Error("RequiresConfirmation() should be false")
	}
	limits := tool.TierLimit()
	if limits == nil || limits["FREE"] != 3 {
		t.Errorf("TierLimit FREE = %v, want 3", limits["FREE"])
	}
}

func TestParseE2BResponseStdout(t *testing.T) {
	body := []byte(`{"results":[],"logs":{"stdout":["hello world\n"],"stderr":[]},"error":{}}`)
	var result e2bExecResult
	parseE2BResponse(body, &result)
	if result.stdout != "hello world\n" {
		t.Errorf("stdout = %q, want %q", result.stdout, "hello world\n")
	}
}

func TestParseE2BResponseImage(t *testing.T) {
	body := []byte(`{"results":[{"text":"<Figure>","png":"iVBORw0KGgoAAAA..."}],"logs":{"stdout":[],"stderr":[]},"error":{}}`)
	var result e2bExecResult
	parseE2BResponse(body, &result)
	if result.imageBase64 == "" {
		t.Error("expected imageBase64 to be populated")
	}
}

func TestParseE2BResponseError(t *testing.T) {
	body := []byte(`{"results":[],"logs":{"stdout":[],"stderr":[]},"error":{"name":"SyntaxError","value":"invalid syntax","traceback":"..."}}`)
	var result e2bExecResult
	parseE2BResponse(body, &result)
	if result.stderr == "" {
		t.Error("expected stderr to contain error info")
	}
}
