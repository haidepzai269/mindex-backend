package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"mindex-backend/config"
)

type CodeSandboxTool struct{}

func NewCodeSandboxTool() *CodeSandboxTool { return &CodeSandboxTool{} }

func (t *CodeSandboxTool) Name() string { return "code_sandbox" }
func (t *CodeSandboxTool) Description() string {
	return "Chạy code Python trong sandbox cô lập (tính toán phức tạp, xử lý data, vẽ chart). Trả về stdout và hình ảnh nếu có. Dùng khi user cần tính toán phức tạp hơn calculator, phân tích dữ liệu, hoặc vẽ biểu đồ."
}
func (t *CodeSandboxTool) Category() ToolCategory { return CategoryComputationExecution }
func (t *CodeSandboxTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"code": map[string]interface{}{
				"type":        "string",
				"description": "Code Python để thực thi. Có sẵn numpy, pandas, matplotlib.",
			},
		},
		"required": []string{"code"},
	}
}
func (t *CodeSandboxTool) Timeout() time.Duration { return 60 * time.Second }
func (t *CodeSandboxTool) TierLimit() map[string]int {
	return map[string]int{"FREE": 3, "PRO": 15, "ULTRA": 30}
}
func (t *CodeSandboxTool) RequiresConfirmation() bool { return false }

type codeSandboxInput struct {
	Code string `json:"code"`
}

func (t *CodeSandboxTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	if config.Env.E2BAPIKey == "" {
		return ToolResult{}, fmt.Errorf("code_sandbox: E2B API key chưa được cấu hình")
	}

	var in codeSandboxInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{}, fmt.Errorf("code_sandbox: input không hợp lệ: %v", err)
	}
	in.Code = strings.TrimSpace(in.Code)
	if in.Code == "" {
		return ToolResult{}, fmt.Errorf("code_sandbox: code không được để trống")
	}

	sandboxID, err := e2bCreateSandbox(ctx)
	if err != nil {
		return ToolResult{}, fmt.Errorf("code_sandbox: không tạo được sandbox: %v", err)
	}
	defer e2bDeleteSandbox(sandboxID)

	result, err := e2bExecuteCode(ctx, sandboxID, in.Code)
	if err != nil {
		return ToolResult{}, fmt.Errorf("code_sandbox: lỗi thực thi: %v", err)
	}

	if result.imageBase64 != "" {
		return ToolResult{
			TextOutput:   result.stdout,
			BinaryOutput: []byte(result.imageBase64),
			BinaryMIME:   "image/png;base64",
		}, nil
	}

	output := result.stdout
	if result.stderr != "" && output == "" {
		output = "Error:\n" + result.stderr
	}
	if output == "" {
		output = "(code chạy thành công, không có output)"
	}

	return ToolResult{TextOutput: output}, nil
}

type e2bExecResult struct {
	stdout      string
	stderr      string
	imageBase64 string
}

func e2bCreateSandbox(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"templateID": "code-interpreter-v1",
		"timeout":    120,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.e2b.dev/sandboxes", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", config.Env.E2BAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("E2B API trả về status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		SandboxID string `json:"sandboxID"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("không parse được response E2B: %v", err)
	}
	if result.SandboxID == "" {
		return "", fmt.Errorf("E2B không trả về sandboxID")
	}
	return result.SandboxID, nil
}

func e2bDeleteSandbox(sandboxID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, "https://api.e2b.dev/sandboxes/"+sandboxID, nil)
	if err != nil {
		return
	}
	req.Header.Set("X-API-Key", config.Env.E2BAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

func e2bExecuteCode(ctx context.Context, sandboxID, code string) (e2bExecResult, error) {
	execURL := fmt.Sprintf("https://%s-49983.e2b.dev/execute", sandboxID)
	body, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, execURL, bytes.NewReader(body))
	if err != nil {
		return e2bExecResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return e2bExecResult{}, fmt.Errorf("không kết nối được sandbox: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return e2bExecResult{}, fmt.Errorf("sandbox trả về status %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	var result e2bExecResult
	parseE2BResponse(respBody, &result)
	return result, nil
}

func parseE2BResponse(body []byte, result *e2bExecResult) {
	var parsed struct {
		Results []struct {
			Text string `json:"text"`
			PNG  string `json:"png"`
		} `json:"results"`
		Logs struct {
			Stdout []string `json:"stdout"`
			Stderr []string `json:"stderr"`
		} `json:"logs"`
		Error struct {
			Name    string `json:"name"`
			Value   string `json:"value"`
			Traceback string `json:"traceback"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		result.stdout = string(body)
		return
	}

	if parsed.Error.Name != "" {
		result.stderr = fmt.Sprintf("%s: %s", parsed.Error.Name, parsed.Error.Value)
		return
	}

	result.stdout = strings.Join(parsed.Logs.Stdout, "")
	result.stderr = strings.Join(parsed.Logs.Stderr, "")

	for _, r := range parsed.Results {
		if r.PNG != "" {
			result.imageBase64 = r.PNG
		}
		if r.Text != "" && result.stdout == "" {
			result.stdout = r.Text
		}
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
