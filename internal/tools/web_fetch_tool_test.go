package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchToolExtractsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>t</title><script>evil()</script></head><body><p>Hello world</p></body></html>`))
	}))
	defer srv.Close()

	tool := NewWebFetchTool()
	input, _ := json.Marshal(map[string]string{"url": srv.URL + "/article"})
	result, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if !strings.Contains(result.TextOutput, "Hello world") {
		t.Fatalf("Execute() = %q, want it to contain %q", result.TextOutput, "Hello world")
	}
	if strings.Contains(result.TextOutput, "evil()") {
		t.Fatalf("Execute() = %q, script content must be stripped", result.TextOutput)
	}
}

func TestWebFetchToolNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tool := NewWebFetchTool()
	input, _ := json.Marshal(map[string]string{"url": srv.URL + "/missing"})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err == nil {
		t.Fatalf("Execute() on 404 page: want error, got nil")
	}
}

func TestWebFetchToolForbiddenByRobotsTxt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte("User-agent: *\nDisallow: /private\n"))
			return
		}
		w.Write([]byte(`<html><body>secret</body></html>`))
	}))
	defer srv.Close()

	tool := NewWebFetchTool()
	input, _ := json.Marshal(map[string]string{"url": srv.URL + "/private/page"})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err == nil {
		t.Fatalf("Execute() on robots.txt-disallowed path: want error, got nil")
	}
}

func TestWebFetchToolInvalidURL(t *testing.T) {
	tool := NewWebFetchTool()
	input, _ := json.Marshal(map[string]string{"url": "not-a-url"})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err == nil {
		t.Fatalf("Execute() with invalid URL: want error, got nil")
	}
}

func TestWebFetchToolTruncatesLongContent(t *testing.T) {
	longText := strings.Repeat("word ", 3000) // well over webFetchMaxTextChars
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body><p>" + longText + "</p></body></html>"))
	}))
	defer srv.Close()

	tool := NewWebFetchTool()
	input, _ := json.Marshal(map[string]string{"url": srv.URL + "/long"})
	result, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if !strings.Contains(result.TextOutput, "đã được cắt bớt") {
		t.Fatalf("Execute() on long content: want truncation marker, got length %d", len(result.TextOutput))
	}
}
