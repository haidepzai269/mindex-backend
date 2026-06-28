package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// WebFetchTool fetches and extracts the readable text content of a single
// user-provided URL (US5), distinct from WebSearchTool which searches the
// open web for a query. Respects robots.txt and truncates very long pages
// (spec Assumption + Edge Case).
type WebFetchTool struct{}

func NewWebFetchTool() *WebFetchTool { return &WebFetchTool{} }

func (t *WebFetchTool) Name() string { return "web_fetch" }
func (t *WebFetchTool) Description() string {
	return "Lấy nội dung văn bản từ một URL cụ thể mà user cung cấp (bài viết, trang web). Dùng khi user paste link và muốn tóm tắt/phân tích."
}
func (t *WebFetchTool) Category() ToolCategory { return CategoryInformationRetrieval }
func (t *WebFetchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "URL đầy đủ (http/https) của trang cần lấy nội dung",
			},
		},
		"required": []string{"url"},
	}
}
func (t *WebFetchTool) Timeout() time.Duration { return 20 * time.Second }
func (t *WebFetchTool) TierLimit() map[string]int {
	return map[string]int{"FREE": 6, "PRO": 20, "ULTRA": 40}
}
func (t *WebFetchTool) RequiresConfirmation() bool { return false }

const webFetchMaxBodyBytes = 3 << 20 // 3 MB, generous cap against huge pages
const webFetchMaxTextChars = 8000    // truncate very long extracted text (Edge Case)

type webFetchInput struct {
	URL string `json:"url"`
}

func (t *WebFetchTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	var in webFetchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{}, fmt.Errorf("web_fetch: invalid input: %v", err)
	}

	parsed, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ToolResult{}, fmt.Errorf("web_fetch: URL không hợp lệ: %q", in.URL)
	}

	if allowed, err := isAllowedByRobotsTxt(ctx, parsed); err == nil && !allowed {
		return ToolResult{}, fmt.Errorf("web_fetch: trang này bị chặn bởi robots.txt")
	}

	body, contentType, err := fetchURL(ctx, parsed.String())
	if err != nil {
		return ToolResult{}, fmt.Errorf("web_fetch: %v", err)
	}

	if !strings.Contains(contentType, "html") && !strings.Contains(contentType, "text") {
		return ToolResult{}, fmt.Errorf("web_fetch: nội dung không phải văn bản/HTML (content-type: %s)", contentType)
	}

	text := extractReadableText(body)
	text = strings.TrimSpace(text)
	if text == "" {
		return ToolResult{}, fmt.Errorf("web_fetch: không lấy được nội dung văn bản từ trang (có thể yêu cầu đăng nhập hoặc render bằng JS)")
	}

	truncated := false
	if len(text) > webFetchMaxTextChars {
		text = text[:webFetchMaxTextChars]
		truncated = true
	}
	if truncated {
		text += "\n\n[... nội dung đã được cắt bớt do quá dài ...]"
	}

	return ToolResult{TextOutput: text}, nil
}

func fetchURL(ctx context.Context, target string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "MindexBot/1.0 (+https://mindex.io.vn)")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("không truy cập được trang: %v", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, "", fmt.Errorf("không tìm thấy trang (404)")
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, "", fmt.Errorf("trang yêu cầu đăng nhập hoặc bị chặn (status %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("trang trả về lỗi (status %d)", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, webFetchMaxBodyBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", fmt.Errorf("lỗi đọc nội dung trang: %v", err)
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// isAllowedByRobotsTxt does a minimal robots.txt check: looks for a
// "User-agent: *" block and rejects the URL if its path matches a Disallow
// prefix in that block. Errors fetching robots.txt are treated as
// "allowed" (most sites have no robots.txt restricting general access).
func isAllowedByRobotsTxt(ctx context.Context, target *url.URL) (bool, error) {
	robotsURL := fmt.Sprintf("%s://%s/robots.txt", target.Scheme, target.Host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return true, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return true, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return true, err
	}

	inWildcardBlock := false
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "user-agent:"):
			agent := strings.TrimSpace(line[len("user-agent:"):])
			inWildcardBlock = agent == "*"
		case inWildcardBlock && strings.HasPrefix(lower, "disallow:"):
			disallowPath := strings.TrimSpace(line[len("disallow:"):])
			if disallowPath != "" && strings.HasPrefix(target.Path, disallowPath) {
				return false, nil
			}
		}
	}
	return true, nil
}

// extractReadableText walks the parsed HTML tree and concatenates visible
// text, skipping <script>/<style>/<noscript> content.
func extractReadableText(body []byte) string {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "head":
				return
			}
		}
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				b.WriteString(text)
				b.WriteString(" ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return b.String()
}
