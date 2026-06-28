package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFileGenerationToolRejectsUnsupportedFormat(t *testing.T) {
	tool := NewFileGenerationTool()
	input, _ := json.Marshal(map[string]string{"format": "docx"})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err == nil {
		t.Fatalf("Execute() with unsupported format: want error, got nil")
	}
}

func TestFileGenerationToolRejectsEmptyPDFContent(t *testing.T) {
	tool := NewFileGenerationTool()
	input, _ := json.Marshal(map[string]string{"format": "pdf", "content": ""})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err == nil {
		t.Fatalf("Execute() with empty PDF content: want error, got nil")
	}
}

func TestFileGenerationToolRejectsEmptyXLSXTable(t *testing.T) {
	tool := NewFileGenerationTool()
	input, _ := json.Marshal(map[string]interface{}{"format": "xlsx", "table": [][]string{}})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err == nil {
		t.Fatalf("Execute() with empty XLSX table: want error, got nil")
	}
}

func TestFileGenerationToolRejectsOversizedContent(t *testing.T) {
	tool := NewFileGenerationTool()
	huge := strings.Repeat("a", fileGenMaxContentChars+1)
	input, _ := json.Marshal(map[string]string{"format": "pdf", "content": huge})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err == nil {
		t.Fatalf("Execute() with oversized content: want error, got nil")
	}
}

// cloudinaryAvailable mirrors the lazy-skip pattern used for DB/Redis-backed
// tests elsewhere in this package: this test hits the real Cloudinary API
// (configured via .env in this environment), so it's an integration test,
// not a pure unit test.
func cloudinaryAvailable(t *testing.T) {
	memoryDBAvailable(t) // reuses the same lazy config.LoadConfig() bootstrap (loads .env)
	if os.Getenv("CLOUDINARY_CLOUD_NAME") == "" {
		t.Skip("Cloudinary not configured in this environment; skipping file_generation upload tests")
	}
}

func TestFileGenerationToolGeneratesAndUploadsPDF(t *testing.T) {
	cloudinaryAvailable(t)
	tool := NewFileGenerationTool()
	input, _ := json.Marshal(map[string]string{
		"format":  "pdf",
		"title":   "Outline: Test Topic",
		"content": "Đoạn 1: giới thiệu.\nĐoạn 2: nội dung chính.\nĐoạn 3: kết luận.",
	})

	result, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if result.FileURL == "" {
		t.Fatalf("Execute() returned empty FileURL")
	}
	assertURLDownloadableAllowingCloudinaryPDFBlock(t, result.FileURL)
}

func TestFileGenerationToolGeneratesAndUploadsXLSX(t *testing.T) {
	cloudinaryAvailable(t)
	tool := NewFileGenerationTool()
	input, _ := json.Marshal(map[string]interface{}{
		"format": "xlsx",
		"title":  "So sánh",
		"table": [][]string{
			{"Tiêu chí", "Lựa chọn A", "Lựa chọn B"},
			{"Giá", "100", "200"},
		},
	})

	result, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if result.FileURL == "" {
		t.Fatalf("Execute() returned empty FileURL")
	}
	assertURLDownloadable(t, result.FileURL)
}

// assertURLDownloadableAllowingCloudinaryPDFBlock tolerates a 401 response,
// which is what this Cloudinary account currently returns for .pdf raw
// resources: Cloudinary's account-level "Restrict delivery of PDF and ZIP
// files" security setting is enabled by default and blocks public delivery
// of uploaded PDFs regardless of upload success. Confirmed via direct curl
// against the returned URL - the upload itself succeeds (FileURL is
// returned), only delivery is blocked. Fix is in the Cloudinary dashboard
// (Settings > Security > "Allow delivery of PDF and ZIP files"), not in
// this code. Any other non-200 status is still a real test failure.
func assertURLDownloadableAllowingCloudinaryPDFBlock(t *testing.T, url string) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("downloading generated file %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Skipf("Cloudinary blocked PDF delivery (401) for %s - this is the account's security setting, not a code defect; enable \"Allow delivery of PDF and ZIP files\" in Cloudinary settings to lift it", url)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("downloading generated file %s: status %d, want 200", url, resp.StatusCode)
	}
}

func assertURLDownloadable(t *testing.T, url string) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("downloading generated file %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("downloading generated file %s: status %d, want 200", url, resp.StatusCode)
	}
}
