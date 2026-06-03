package utils

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mindex-backend/config"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	WebSearchProviderSerper    = "serper"
	WebSearchProviderTavily    = "tavily"
	WebSearchProviderBrave     = "brave"
	WebSearchProviderExa       = "exa"
	WebSearchProviderGoogleCSE = "google_cse"
	WebSearchProviderDDG       = "ddg"
)

type WebSearchPlan struct {
	UseWebSearch bool   `json:"use_web_search"`
	Query        string `json:"query"`
	ContentType  string `json:"content_type"`
	Reason       string `json:"reason"`
}

type WebSearchResult struct {
	Title    string  `json:"title"`
	URL      string  `json:"url"`
	Snippet  string  `json:"snippet"`
	Content  string  `json:"content"`
	Provider string  `json:"provider"`
	Score    float64 `json:"score,omitempty"`
}

type WebSearchResponse struct {
	Provider  string            `json:"provider"`
	Query     string            `json:"query"`
	Results   []WebSearchResult `json:"results"`
	FromCache bool              `json:"from_cache"`
}

type WebSearchQuotaStatus struct {
	Provider         string `json:"provider"`
	Priority         int    `json:"priority"`
	Configured       bool   `json:"configured"`
	Available        bool   `json:"available"`
	MonthlyUsed      int64  `json:"monthly_used"`
	MonthlySafe      int    `json:"monthly_safe,omitempty"`
	MonthlyHard      int    `json:"monthly_hard,omitempty"`
	DailyUsed        int64  `json:"daily_used"`
	DailySafe        int    `json:"daily_safe,omitempty"`
	DailyHard        int    `json:"daily_hard,omitempty"`
	RemainingMonthly int64  `json:"remaining_monthly,omitempty"`
	RemainingDaily   int64  `json:"remaining_daily,omitempty"`
}

type webSearchProviderConfig struct {
	Name        string
	Priority    int
	Keys        []string
	MonthlySafe int
	MonthlyHard int
	DailySafe   int
	DailyHard   int
}

func DecideWebSearch(question, searchQuery, contextText, docIntel, personaName, tier string, thinking bool) WebSearchPlan {
	heuristic := heuristicWebSearchPlan(question, searchQuery, contextText, docIntel, personaName, tier, thinking)

	// Nếu heuristic đã confident true → return ngay, không cần LLM
	if heuristic.UseWebSearch {
		return heuristic
	}

	// Chỉ gọi LLM cho "gray zone": thinking mode + paid tier
	// (user yêu cầu phân tích sâu, heuristic không bắt được intent)
	if AI == nil || !thinking || !isPaidTier(tier) {
		return heuristic
	}

	prompt := fmt.Sprintf(`Bạn là bộ định tuyến web search cho hệ thống chat RAG trên tài liệu.

Nhiệm vụ: quyết định có cần gọi web search bên ngoài không.

Quy tắc bắt buộc:
- Chỉ search khi thông tin cần xác minh/cập nhật từ web và liên quan tới câu hỏi hoặc tài liệu.
- Với doctor/legal: chỉ search nếu câu hỏi liên quan rõ tới tài liệu/ngữ cảnh; không dùng web search cho yêu cầu ngoài chủ đề tài liệu.
- thinking=true nghĩa là user yêu cầu phân tích sâu hơn; khi có dấu hiệu cần xác minh, tiêu chuẩn, luật, dữ liệu mới, số liệu, trạng thái hiện hành, hãy nghiêng về use_web_search=true.
- Nếu câu trả lời đã nằm rõ trong context tài liệu và không cần cập nhật, use_web_search=false.
- Trả về JSON duy nhất, không markdown.

JSON schema:
{"use_web_search":true|false,"query":"query ngắn để search","content_type":"price|news|regulation|concept|default","reason":"lý do ngắn"}

Tier: %s
Persona: %s
Thinking: %t

[DOCUMENT MAP]
%s

[RAG CONTEXT SNIPPET]
%s

[USER QUESTION]
%s

[RETRIEVAL QUERY]
%s`, tier, personaName, thinking, trimForPrompt(docIntel, 800), trimForPrompt(contextText, 2600), question, searchQuery)

	answer, _, err := AI.ChatNonStream(ServiceSearch, []ChatMessage{
		{Role: "system", Content: "Bạn chỉ trả về JSON hợp lệ cho quyết định web search."},
		{Role: "user", Content: prompt},
	})
	if err != nil {
		log.Printf("⚠️ [WebSearchDecision] LLM decision failed: %v. Using heuristic.", err)
		return heuristic
	}

	var plan WebSearchPlan
	if err := json.Unmarshal([]byte(extractJSONObject(answer)), &plan); err != nil {
		log.Printf("⚠️ [WebSearchDecision] Invalid JSON decision: %v. Raw=%q", err, answer)
		return heuristic
	}

	plan.Query = strings.TrimSpace(plan.Query)
	if plan.Query == "" {
		plan.Query = fallbackSearchQuery(question, searchQuery)
	}
	if plan.ContentType == "" {
		plan.ContentType = heuristic.ContentType
	}
	if isSensitivePersona(personaName) && !looksRelatedToContext(question+" "+plan.Query, contextText+" "+docIntel) {
		plan.UseWebSearch = false
		plan.Reason = "Sensitive persona: query is not clearly related to the provided document context"
	}
	return plan
}

// WebSearchHeuristicTriggered kiểm tra nhanh xem câu hỏi có trigger web search không
// dựa chỉ trên keyword (không cần context/docIntel). Dùng để quyết định có skip
// off-topic short-circuit không khi maxVectorSim thấp.
func WebSearchHeuristicTriggered(question, searchQuery string) bool {
	plan := heuristicWebSearchPlan(question, searchQuery, "", "", "", "", false)
	return plan.UseWebSearch
}

func SearchWeb(ctx context.Context, plan WebSearchPlan) (*WebSearchResponse, error) {
	if !config.Env.WebSearchEnabled {
		return nil, errors.New("web search is disabled")
	}
	query := strings.TrimSpace(plan.Query)
	if query == "" {
		return nil, errors.New("web search query is empty")
	}

	maxResults := config.Env.WebSearchMaxResults
	if maxResults <= 0 || maxResults > 10 {
		maxResults = 5
	}

	cacheKey := webSearchCacheKey(query, plan.ContentType, maxResults)
	if cached, ok := getCachedWebSearch(ctx, cacheKey); ok {
		cached.FromCache = true
		return cached, nil
	}

	var lastErr error
	for _, provider := range orderedWebSearchProviders() {
		if !isWebSearchQuotaAvailable(ctx, provider) {
			continue
		}
		if !isWebSearchProviderConfigured(provider) {
			continue
		}

		results, err := callWebSearchProvider(ctx, provider, query, maxResults)
		if err != nil {
			lastErr = err
			log.Printf("⚠️ [WebSearch] provider=%s failed: %v", provider.Name, err)
			continue
		}
		if len(results) == 0 {
			lastErr = fmt.Errorf("provider %s returned no results", provider.Name)
			continue
		}

		incrementWebSearchUsage(ctx, provider.Name)
		resp := &WebSearchResponse{
			Provider: provider.Name,
			Query:    query,
			Results:  results,
		}
		setCachedWebSearch(ctx, cacheKey, resp, webSearchCacheTTL(plan.ContentType))
		return resp, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no web search provider is available")
}

func CheckWebSearchUserLimit(ctx context.Context, userID, tier string) (bool, time.Duration) {
	if config.RedisClient == nil || strings.TrimSpace(userID) == "" {
		return true, 0
	}
	limit, window := webSearchUserLimit(tier)
	key := fmt.Sprintf("websearch:userlimit:%s:%s", strings.ToUpper(strings.TrimSpace(tier)), userID)
	used, err := config.RedisClient.Incr(ctx, key).Result()
	if err != nil {
		return true, 0
	}
	if used == 1 {
		config.RedisClient.Expire(ctx, key, window)
	}
	if used <= int64(limit) {
		return true, 0
	}
	ttl, err := config.RedisClient.TTL(ctx, key).Result()
	if err != nil || ttl < 0 {
		ttl = window
	}
	return false, ttl
}

func FormatWebSearchContext(resp *WebSearchResponse) string {
	if resp == nil || len(resp.Results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[WEB SEARCH CONTEXT]\n")
	b.WriteString("Các nguồn dưới đây đến từ web search bên ngoài tài liệu. Chỉ dùng để xác minh hoặc bổ sung thông tin liên quan; luôn nêu nguồn khi dùng.\n")
	b.WriteString(fmt.Sprintf("Provider: %s | Query: %s\n\n", resp.Provider, resp.Query))
	for i, r := range resp.Results {
		body := r.Content
		if body == "" {
			body = r.Snippet
		}
		b.WriteString(fmt.Sprintf("[%d] %s\nURL: %s\n%s\n\n", i+1, r.Title, r.URL, trimForPrompt(body, 900)))
	}
	return b.String()
}

func WebSearchSources(resp *WebSearchResponse) []map[string]interface{} {
	if resp == nil {
		return nil
	}
	sources := make([]map[string]interface{}, 0, len(resp.Results))
	for i, r := range resp.Results {
		content := r.Content
		if content == "" {
			content = r.Snippet
		}
		sources = append(sources, map[string]interface{}{
			"type":     "web",
			"rank":     i + 1,
			"title":    r.Title,
			"url":      r.URL,
			"provider": r.Provider,
			"content":  content,
			"score":    r.Score,
		})
	}
	return sources
}

func GetWebSearchQuotaStatus(ctx context.Context) []WebSearchQuotaStatus {
	providers := orderedWebSearchProviders()
	statuses := make([]WebSearchQuotaStatus, 0, len(providers))
	for _, p := range providers {
		monthly, daily := getWebSearchUsage(ctx, p.Name)
		status := WebSearchQuotaStatus{
			Provider:    p.Name,
			Priority:    p.Priority,
			Configured:  isWebSearchProviderConfigured(p),
			Available:   isWebSearchProviderConfigured(p) && isWebSearchQuotaAvailable(ctx, p),
			MonthlyUsed: monthly,
			MonthlySafe: p.MonthlySafe,
			MonthlyHard: p.MonthlyHard,
			DailyUsed:   daily,
			DailySafe:   p.DailySafe,
			DailyHard:   p.DailyHard,
		}
		if p.MonthlySafe > 0 {
			status.RemainingMonthly = maxInt64(int64(p.MonthlySafe)-monthly, 0)
		}
		if p.DailySafe > 0 {
			status.RemainingDaily = maxInt64(int64(p.DailySafe)-daily, 0)
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func heuristicWebSearchPlan(question, searchQuery, contextText, docIntel, personaName, tier string, thinking bool) WebSearchPlan {
	q := strings.ToLower(question + " " + searchQuery)
	plan := WebSearchPlan{
		UseWebSearch: false,
		Query:        fallbackSearchQuery(question, searchQuery),
		ContentType:  "default",
		Reason:       "No web search trigger matched",
	}

	if isSensitivePersona(personaName) && !looksRelatedToContext(q, strings.ToLower(contextText+" "+docIntel)) {
		plan.Reason = "Sensitive persona requires document-related web search"
		return plan
	}

	timeTriggers := []string{"hiện tại", "hiện nay", "thời điểm hiện nay", "mới nhất", "hôm nay", "current", "latest", "update", "updated", "2025", "2026", "bây giờ", "recent"}
	numericTriggers := []string{"giá", "lãi suất", "tỷ giá", "tỉ giá", "thống kê", "%", "usd", "vnd", "vnđ", "rate", "price", "statistic"}
	regulationTriggers := []string{"luật", "nghị định", "thông tư", "quy định", "tiêu chuẩn", "iso", "policy", "regulation", "standard", "compliance"}
	verificationTriggers := []string{"tìm kiếm trên web", "tra cứu web", "web search", "kiểm tra", "xác minh", "đối chiếu", "nguồn", "citation", "verify", "fact check", "so sánh với hiện nay"}

	switch {
	case containsAny(q, regulationTriggers):
		plan.UseWebSearch = true
		plan.ContentType = "regulation"
		plan.Reason = "Question may need current rules, standards, or compliance status"
	case containsAny(q, timeTriggers):
		plan.UseWebSearch = true
		plan.ContentType = "news"
		plan.Reason = "Question asks for current or recent information"
	case containsAny(q, numericTriggers):
		plan.UseWebSearch = true
		plan.ContentType = "price"
		plan.Reason = "Question asks for current numeric data"
	case thinking && isPaidTier(tier) && containsAny(q, verificationTriggers):
		plan.UseWebSearch = true
		plan.ContentType = "default"
		plan.Reason = "Thinking mode requested deeper verification"
	case thinking && isPaidTier(tier) && strings.TrimSpace(contextText) == "":
		plan.UseWebSearch = true
		plan.ContentType = "default"
		plan.Reason = "Thinking mode with no retrieved document context"
	}
	return plan
}

func orderedWebSearchProviders() []webSearchProviderConfig {
	configs := map[string]webSearchProviderConfig{
		WebSearchProviderSerper: {
			Name: WebSearchProviderSerper, Keys: config.Env.SerperKeys,
			MonthlySafe: config.Env.SerperMonthlySafe, MonthlyHard: 2500,
		},
		WebSearchProviderTavily: {
			Name: WebSearchProviderTavily, Keys: config.Env.TavilyKeys,
			MonthlySafe: config.Env.TavilyMonthlySafe, MonthlyHard: 1000,
		},
		// Brave Search is intentionally disabled for now. Keep callBrave below
		// so it can be restored quickly when BRAVE_SEARCH_API_KEYS is ready.
		// WebSearchProviderBrave: {
		// 	Name: WebSearchProviderBrave, Keys: config.Env.BraveSearchKeys,
		// 	MonthlySafe: config.Env.BraveSearchMonthlySafe, MonthlyHard: 1000,
		// },
		WebSearchProviderExa: {
			Name: WebSearchProviderExa, Keys: config.Env.ExaKeys,
			MonthlySafe: config.Env.ExaMonthlySafe, MonthlyHard: 1000,
		},
		WebSearchProviderGoogleCSE: {
			Name:        WebSearchProviderGoogleCSE,
			Keys:        []string{config.Env.GoogleCSEAPIKey},
			MonthlySafe: config.Env.GoogleCSEMonthlySafe, MonthlyHard: 3000,
			DailySafe: config.Env.GoogleCSEDailySafe, DailyHard: 100,
		},
		WebSearchProviderDDG: {
			Name: WebSearchProviderDDG,
		},
	}

	var providers []webSearchProviderConfig
	seen := map[string]bool{}
	for idx, name := range config.Env.WebSearchProviderOrder {
		normalized := normalizeWebSearchProvider(name)
		p, ok := configs[normalized]
		if !ok || seen[normalized] {
			continue
		}
		p.Priority = idx + 1
		providers = append(providers, p)
		seen[normalized] = true
	}

	for name, p := range configs {
		if seen[name] {
			continue
		}
		p.Priority = len(providers) + 1
		providers = append(providers, p)
	}
	sort.SliceStable(providers, func(i, j int) bool {
		return providers[i].Priority < providers[j].Priority
	})
	return providers
}

func callWebSearchProvider(ctx context.Context, provider webSearchProviderConfig, query string, maxResults int) ([]WebSearchResult, error) {
	switch provider.Name {
	case WebSearchProviderSerper:
		return callSerper(ctx, pickWebSearchKey(provider), query, maxResults)
	case WebSearchProviderTavily:
		return callTavily(ctx, pickWebSearchKey(provider), query, maxResults)
	// case WebSearchProviderBrave:
	// 	return callBrave(ctx, pickWebSearchKey(provider), query, maxResults)
	case WebSearchProviderExa:
		return callExa(ctx, pickWebSearchKey(provider), query, maxResults)
	case WebSearchProviderGoogleCSE:
		return callGoogleCSE(ctx, config.Env.GoogleCSEAPIKey, config.Env.GoogleCSECX, query, maxResults)
	case WebSearchProviderDDG:
		return callDuckDuckGo(ctx, query, maxResults)
	default:
		return nil, fmt.Errorf("unknown web search provider %s", provider.Name)
	}
}

func callSerper(ctx context.Context, apiKey, query string, maxResults int) ([]WebSearchResult, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"q":   query,
		"gl":  config.Env.WebSearchDefaultGL,
		"hl":  config.Env.WebSearchDefaultHL,
		"num": maxResults,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://google.serper.dev/search", bytes.NewReader(body))
	req.Header.Set("X-API-KEY", apiKey)
	req.Header.Set("Content-Type", "application/json")

	var payload struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"organic"`
	}
	if err := doJSON(req, &payload); err != nil {
		return nil, err
	}
	results := make([]WebSearchResult, 0, len(payload.Organic))
	for _, item := range payload.Organic {
		results = append(results, WebSearchResult{Title: item.Title, URL: item.Link, Snippet: item.Snippet, Content: item.Snippet, Provider: WebSearchProviderSerper})
	}
	return limitWebResults(results, maxResults), nil
}

func callTavily(ctx context.Context, apiKey, query string, maxResults int) ([]WebSearchResult, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"query":        query,
		"search_depth": "basic",
		"max_results":  maxResults,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	var payload struct {
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := doJSON(req, &payload); err != nil {
		return nil, err
	}
	results := make([]WebSearchResult, 0, len(payload.Results))
	for _, item := range payload.Results {
		results = append(results, WebSearchResult{Title: item.Title, URL: item.URL, Snippet: item.Content, Content: item.Content, Provider: WebSearchProviderTavily, Score: item.Score})
	}
	return limitWebResults(results, maxResults), nil
}

func callBrave(ctx context.Context, apiKey, query string, maxResults int) ([]WebSearchResult, error) {
	u := "https://api.search.brave.com/res/v1/web/search?q=" + url.QueryEscape(query) + fmt.Sprintf("&count=%d", maxResults)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)

	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := doJSON(req, &payload); err != nil {
		return nil, err
	}
	results := make([]WebSearchResult, 0, len(payload.Web.Results))
	for _, item := range payload.Web.Results {
		results = append(results, WebSearchResult{Title: item.Title, URL: item.URL, Snippet: item.Description, Content: item.Description, Provider: WebSearchProviderBrave})
	}
	return limitWebResults(results, maxResults), nil
}

func callExa(ctx context.Context, apiKey, query string, maxResults int) ([]WebSearchResult, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"query":      query,
		"numResults": maxResults,
		"contents": map[string]interface{}{
			"text": true,
		},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.exa.ai/search", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	var payload struct {
		Results []struct {
			Title string  `json:"title"`
			URL   string  `json:"url"`
			Text  string  `json:"text"`
			Score float64 `json:"score"`
		} `json:"results"`
	}
	if err := doJSON(req, &payload); err != nil {
		return nil, err
	}
	results := make([]WebSearchResult, 0, len(payload.Results))
	for _, item := range payload.Results {
		results = append(results, WebSearchResult{Title: item.Title, URL: item.URL, Snippet: item.Text, Content: item.Text, Provider: WebSearchProviderExa, Score: item.Score})
	}
	return limitWebResults(results, maxResults), nil
}

func callGoogleCSE(ctx context.Context, apiKey, cx, query string, maxResults int) ([]WebSearchResult, error) {
	values := url.Values{}
	values.Set("key", apiKey)
	values.Set("cx", cx)
	values.Set("q", query)
	values.Set("num", fmt.Sprintf("%d", maxResults))
	values.Set("gl", config.Env.WebSearchDefaultGL)
	values.Set("hl", config.Env.WebSearchDefaultHL)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/customsearch/v1?"+values.Encode(), nil)

	var payload struct {
		Items []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"items"`
	}
	if err := doJSON(req, &payload); err != nil {
		return nil, err
	}
	results := make([]WebSearchResult, 0, len(payload.Items))
	for _, item := range payload.Items {
		results = append(results, WebSearchResult{Title: item.Title, URL: item.Link, Snippet: item.Snippet, Content: item.Snippet, Provider: WebSearchProviderGoogleCSE})
	}
	return limitWebResults(results, maxResults), nil
}

func callDuckDuckGo(ctx context.Context, query string, maxResults int) ([]WebSearchResult, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://duckduckgo.com/html/?q="+url.QueryEscape(query), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 MindexBot/1.0")
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ddg failed with status %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	htmlBody := string(raw)
	linkRe := regexp.MustCompile(`(?is)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	snippetRe := regexp.MustCompile(`(?is)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>|<div[^>]+class="result__snippet"[^>]*>(.*?)</div>`)
	links := linkRe.FindAllStringSubmatch(htmlBody, maxResults)
	snippets := snippetRe.FindAllStringSubmatch(htmlBody, maxResults)

	results := make([]WebSearchResult, 0, len(links))
	for i, item := range links {
		snippet := ""
		if i < len(snippets) {
			snippet = cleanHTML(firstNonEmpty(snippets[i][1:]...))
		}
		results = append(results, WebSearchResult{
			Title:    cleanHTML(item[2]),
			URL:      html.UnescapeString(item[1]),
			Snippet:  snippet,
			Content:  snippet,
			Provider: WebSearchProviderDDG,
		})
	}
	return limitWebResults(results, maxResults), nil
}

func doJSON(req *http.Request, out interface{}) error {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("web search API failed with status %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func isWebSearchProviderConfigured(p webSearchProviderConfig) bool {
	switch p.Name {
	case WebSearchProviderDDG:
		return true
	case WebSearchProviderGoogleCSE:
		return config.Env.GoogleCSEAPIKey != "" && config.Env.GoogleCSECX != ""
	default:
		return len(p.Keys) > 0
	}
}

func isWebSearchQuotaAvailable(ctx context.Context, p webSearchProviderConfig) bool {
	monthly, daily := getWebSearchUsage(ctx, p.Name)
	if p.MonthlySafe > 0 && monthly >= int64(p.MonthlySafe) {
		return false
	}
	if p.DailySafe > 0 && daily >= int64(p.DailySafe) {
		return false
	}
	return true
}

func getWebSearchUsage(ctx context.Context, provider string) (int64, int64) {
	if config.RedisClient == nil {
		return 0, 0
	}
	monthly, _ := config.RedisClient.Get(ctx, webSearchMonthKey(provider)).Int64()
	daily, _ := config.RedisClient.Get(ctx, webSearchDayKey(provider)).Int64()
	return monthly, daily
}

func incrementWebSearchUsage(ctx context.Context, provider string) {
	if config.RedisClient == nil {
		return
	}
	mk := webSearchMonthKey(provider)
	dk := webSearchDayKey(provider)
	config.RedisClient.Incr(ctx, mk)
	config.RedisClient.Expire(ctx, mk, 35*24*time.Hour)
	config.RedisClient.Incr(ctx, dk)
	config.RedisClient.Expire(ctx, dk, 25*time.Hour)
}

func webSearchMonthKey(provider string) string {
	return fmt.Sprintf("websearch:quota:%s:monthly:%s", provider, time.Now().Format("2006-01"))
}

func webSearchDayKey(provider string) string {
	return fmt.Sprintf("websearch:quota:%s:daily:%s", provider, time.Now().Format("2006-01-02"))
}

func pickWebSearchKey(p webSearchProviderConfig) string {
	if len(p.Keys) == 0 {
		return ""
	}
	return p.Keys[int(time.Now().UnixNano()%int64(len(p.Keys)))]
}

func webSearchCacheKey(query, contentType string, maxResults int) string {
	raw := strings.Join([]string{
		canonicalWebSearchQuery(query),
		contentType,
		config.Env.WebSearchDefaultGL,
		config.Env.WebSearchDefaultHL,
		fmt.Sprintf("%d", maxResults),
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return "websearch:cache:v1:" + hex.EncodeToString(sum[:])
}

func canonicalWebSearchQuery(query string) string {
	q := strings.ToLower(RemoveVietnameseSigns(query))
	replacer := strings.NewReplacer(
		"security operations center", "soc",
		"tìm kiếm trên web", " ",
		"tim kiem tren web", " ",
		"tra cứu web", " ",
		"tra cuu web", " ",
		"web search", " ",
		"giúp tôi", " ",
		"giup toi", " ",
		"cho tôi", " ",
		"cho toi", " ",
		"tính đến thời điểm hiện nay", " ",
		"tinh den thoi diem hien nay", " ",
		"tính đến thời điểm hiện tại", " ",
		"tinh den thoi diem hien tai", " ",
		"thời điểm hiện nay", " ",
		"thoi diem hien nay", " ",
		"thời điểm hiện tại", " ",
		"thoi diem hien tai", " ",
		"hiện nay", " ",
		"hien nay", " ",
		"hiện tại", " ",
		"hien tai", " ",
		"mới nhất", " ",
		"moi nhat", " ",
	)
	q = replacer.Replace(q)
	words := strings.FieldsFunc(q, func(r rune) bool {
		return r < '0' || (r > '9' && r < 'a') || r > 'z'
	})
	stop := map[string]bool{
		"cac": true, "trang": true, "web": true, "website": true, "nao": true,
		"la": true, "ve": true, "cua": true, "cho": true, "toi": true, "ban": true,
		"nhung": true, "danh": true, "sach": true, "tot": true, "nhat": true,
		"chuan": true, "hien": true, "nay": true, "tai": true, "thoi": true, "diem": true,
		"the": true, "best": true, "top": true, "current": true, "latest": true,
	}
	uniq := map[string]bool{}
	var kept []string
	for _, w := range words {
		if len(w) < 3 || stop[w] {
			continue
		}
		if !uniq[w] {
			uniq[w] = true
			kept = append(kept, w)
		}
	}
	sort.Strings(kept)
	if len(kept) == 0 {
		return strings.Join(words, " ")
	}
	return strings.Join(kept, " ")
}

func getCachedWebSearch(ctx context.Context, key string) (*WebSearchResponse, bool) {
	if config.RedisClient == nil {
		return nil, false
	}
	raw, err := config.RedisClient.Get(ctx, key).Result()
	if err != nil || raw == "" {
		return nil, false
	}
	var resp WebSearchResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return nil, false
	}
	return &resp, true
}

func setCachedWebSearch(ctx context.Context, key string, resp *WebSearchResponse, ttl time.Duration) {
	if config.RedisClient == nil || resp == nil {
		return
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return
	}
	config.RedisClient.Set(ctx, key, string(raw), ttl)
}

func webSearchCacheTTL(contentType string) time.Duration {
	switch contentType {
	case "price":
		return 15 * time.Minute
	case "news":
		return time.Hour
	case "regulation":
		return 24 * time.Hour
	case "concept":
		return 7 * 24 * time.Hour
	default:
		return 30 * time.Minute
	}
}

func webSearchUserLimit(tier string) (int, time.Duration) {
	switch strings.ToUpper(strings.TrimSpace(tier)) {
	case "ULTRA", "ADMIN":
		return 40, time.Hour
	case "PRO":
		return 20, time.Hour
	default:
		return 6, time.Hour
	}
}

func normalizeWebSearchProvider(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "brave_search":
		return WebSearchProviderBrave
	case "google", "google_cse", "google_custom_search":
		return WebSearchProviderGoogleCSE
	case "duckduckgo", "ddg":
		return WebSearchProviderDDG
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

func isPaidTier(tier string) bool {
	tier = strings.ToUpper(strings.TrimSpace(tier))
	return tier == "PRO" || tier == "ULTRA" || tier == "ADMIN"
}

func isSensitivePersona(personaName string) bool {
	p := strings.ToLower(strings.TrimSpace(personaName))
	return p == "doctor" || p == "legal"
}

func containsAny(s string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func looksRelatedToContext(query, ctx string) bool {
	query = strings.ToLower(RemoveVietnameseSigns(query))
	ctx = strings.ToLower(RemoveVietnameseSigns(ctx))
	if strings.TrimSpace(ctx) == "" {
		return false
	}
	words := strings.FieldsFunc(query, func(r rune) bool {
		return r < '0' || (r > '9' && r < 'a') || r > 'z'
	})
	matches := 0
	for _, w := range words {
		if len(w) < 4 {
			continue
		}
		if strings.Contains(ctx, w) {
			matches++
			if matches >= 2 {
				return true
			}
		}
	}
	return false
}

func fallbackSearchQuery(question, searchQuery string) string {
	if strings.TrimSpace(searchQuery) != "" {
		return strings.TrimSpace(searchQuery)
	}
	return strings.TrimSpace(question)
}

func trimForPrompt(s string, maxRunes int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= maxRunes {
		return string(rs)
	}
	return string(rs[:maxRunes]) + "..."
}

func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		return s
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

func limitWebResults(results []WebSearchResult, maxResults int) []WebSearchResult {
	cleaned := make([]WebSearchResult, 0, len(results))
	for _, r := range results {
		r.Title = strings.TrimSpace(r.Title)
		r.URL = strings.TrimSpace(r.URL)
		r.Snippet = strings.TrimSpace(r.Snippet)
		r.Content = strings.TrimSpace(r.Content)
		if r.Title == "" || r.URL == "" {
			continue
		}
		cleaned = append(cleaned, r)
	}
	if len(cleaned) > maxResults {
		return cleaned[:maxResults]
	}
	return cleaned
}

func cleanHTML(s string) string {
	tagRe := regexp.MustCompile(`(?is)<[^>]+>`)
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
