package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mindex-backend/utils"
)

// richContentTool is a stub that returns RichContent, used to test multi-tool
// rich content aggregation (T039).
type richContentTool struct {
	name        string
	richType    string
	richData    string
}

func (t *richContentTool) Name() string        { return t.name }
func (t *richContentTool) Description() string { return "stub" }
func (t *richContentTool) Category() ToolCategory { return CategoryInformationRetrieval }
func (t *richContentTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (t *richContentTool) Timeout() time.Duration   { return 5 * time.Second }
func (t *richContentTool) TierLimit() map[string]int { return nil }
func (t *richContentTool) RequiresConfirmation() bool { return false }
func (t *richContentTool) Execute(_ context.Context, _ ToolExecutionContext, _ json.RawMessage) (ToolResult, error) {
	rc, _ := json.Marshal(map[string]interface{}{
		"type":      t.richType,
		"data":      json.RawMessage(t.richData),
		"timestamp": "2026-06-28T00:00:00Z",
	})
	return ToolResult{
		JSONOutput:  json.RawMessage(t.richData),
		RichContent: rc,
	}, nil
}

// T039: Multi-domain query should trigger 2 tool calls and return 2 RichContents
func TestDispatcherMultiToolRichContents(t *testing.T) {
	r := NewRegistry()
	r.Register(&richContentTool{
		name:     "weather",
		richType: "weather",
		richData: `{"city":"Saigon","temperature":32}`,
	})
	r.Register(&richContentTool{
		name:     "crypto",
		richType: "crypto",
		richData: `{"name":"Bitcoin","price_vnd":2450000000}`,
	})

	callCount := 0
	d := &Dispatcher{
		Registry: r,
		chatFunc: func(_ utils.ServiceType, msgs []utils.ChatMessage, _ []utils.ToolSchema) (string, []utils.ToolCallRequest, utils.ProviderType, error) {
			callCount++
			if callCount == 1 {
				return "", []utils.ToolCallRequest{
					{ID: "call1", Name: "weather", Arguments: `{"city":"Sài Gòn"}`},
					{ID: "call2", Name: "crypto", Arguments: `{"coin":"BTC"}`},
				}, "test", nil
			}
			return "Thời tiết Sài Gòn 32°C và giá BTC 2.45 tỷ VND.", nil, "test", nil
		},
	}

	result, err := d.Run(context.Background(), ToolExecutionContext{UserID: "u1"}, nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(result.ToolCalls))
	}

	if len(result.RichContents) != 2 {
		t.Fatalf("expected 2 RichContents, got %d", len(result.RichContents))
	}

	if result.RichContent == nil {
		t.Fatal("RichContent (backward compat) should not be nil")
	}

	types := make([]string, 0, 2)
	for _, rc := range result.RichContents {
		var parsed struct {
			Type string `json:"type"`
		}
		json.Unmarshal(rc, &parsed)
		types = append(types, parsed.Type)
	}
	if types[0] != "weather" || types[1] != "crypto" {
		t.Errorf("RichContents types = %v, want [weather, crypto]", types)
	}
}

func TestCollectRichContentsEmpty(t *testing.T) {
	records := []ToolCallRecord{
		{ToolName: "calculator", Status: "success"},
	}
	rcs := collectRichContents(records)
	if len(rcs) != 0 {
		t.Errorf("expected 0 RichContents for non-rich tool, got %d", len(rcs))
	}
}

func TestCollectRichContentsMixed(t *testing.T) {
	records := []ToolCallRecord{
		{ToolName: "weather", Status: "success", RichContent: json.RawMessage(`{"type":"weather"}`)},
		{ToolName: "calculator", Status: "success"},
		{ToolName: "crypto", Status: "success", RichContent: json.RawMessage(`{"type":"crypto"}`)},
	}
	rcs := collectRichContents(records)
	if len(rcs) != 2 {
		t.Fatalf("expected 2 RichContents, got %d", len(rcs))
	}
}

// T040: Vietnamese city name edge cases
func TestVietnameseCityNameVariations(t *testing.T) {
	variations := map[string][]string{
		"ho chi minh city": {"Hồ Chí Minh", "Ho Chi Minh", "Sài Gòn", "Saigon", "HCMC"},
		"ha noi":           {"Hà Nội", "Ha Noi", "Hanoi"},
		"da nang":          {"Đà Nẵng", "Da Nang", "Danang"},
	}

	for _, names := range variations {
		hashes := make(map[string]bool)
		for _, name := range names {
			lower := strings.ToLower(name)
			hash := HashQuery("weather", lower)
			hashes[hash] = true
			if len(hash) != 16 {
				t.Errorf("HashQuery(%q) length = %d, want 16", name, len(hash))
			}
		}
		// Different spellings produce different hashes (expected - API handles resolution)
		// The point is that each variation can be URL-encoded and sent to the API
	}
}

func TestWeatherToolAcceptsVietnameseNames(t *testing.T) {
	tool := NewWeatherTool()
	names := []string{"Hà Nội", "Hồ Chí Minh", "Đà Nẵng", "Sài Gòn", "Cần Thơ", "Huế"}

	for _, name := range names {
		input, _ := json.Marshal(map[string]string{"city": name})
		// Execute will fail without API keys, but should not reject the input
		_, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
		if err != nil {
			errMsg := err.Error()
			// API connection errors are expected (no API key in test env)
			// Input validation errors are NOT expected
			if strings.Contains(errMsg, "thiếu tên thành phố") ||
				strings.Contains(errMsg, "tên thành phố quá dài") ||
				strings.Contains(errMsg, "tham số không hợp lệ") {
				t.Errorf("weather tool rejected valid Vietnamese city name %q: %v", name, err)
			}
		}
	}
}

// T041: Coin name resolution edge cases
func TestCoinResolutionCaseInsensitive(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"BTC", "bitcoin"},
		{"btc", "bitcoin"},
		{"Btc", "bitcoin"},
		{"Bitcoin", "bitcoin"},
		{"bitcoin", "bitcoin"},
		{"BITCOIN", "bitcoin"},
		{"ETH", "ethereum"},
		{"eth", "ethereum"},
		{"Ethereum", "ethereum"},
		{"SOL", "solana"},
		{"sol", "solana"},
		{"Solana", "solana"},
	}

	for _, tc := range cases {
		got := resolveCoinID(tc.input)
		if got != tc.expected {
			t.Errorf("resolveCoinID(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestCoinResolutionWithWhitespace(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"  BTC  ", "bitcoin"},
		{" bitcoin ", "bitcoin"},
		{"  ETH", "ethereum"},
	}

	for _, tc := range cases {
		got := resolveCoinID(tc.input)
		if got != tc.expected {
			t.Errorf("resolveCoinID(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestCoinResolutionUnknownPassesThrough(t *testing.T) {
	got := resolveCoinID("some-unknown-coin")
	if got != "some-unknown-coin" {
		t.Errorf("resolveCoinID('some-unknown-coin') = %q, want passthrough", got)
	}
}

// T035: Verify tool prompt includes all registered tools
func TestToolPromptIncludesAllTools(t *testing.T) {
	r := NewRegistry()
	RegisterDefaultTools(r)

	prompt := BuildToolUsagePrompt(r)

	expectedTools := []string{"weather", "news", "crypto", "calculator", "web_search"}
	for _, toolName := range expectedTools {
		if !strings.Contains(prompt, toolName) {
			t.Errorf("tool prompt missing registered tool %q", toolName)
		}
	}

	if !strings.Contains(prompt, "AVAILABLE TOOLS") {
		t.Error("tool prompt missing header")
	}
	if !strings.Contains(prompt, "KHÔNG BAO GIỜ tự bịa") {
		t.Error("tool prompt missing anti-hallucination rule")
	}
}
