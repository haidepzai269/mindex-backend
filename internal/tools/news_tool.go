package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"mindex-backend/config"
)

const (
	newsCacheTTL         = 30 * time.Minute
	newsProviderTimeout  = 2 * time.Second
)

type NewsItem struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Source      string `json:"source"`
	URL        string `json:"url"`
	ImageURL   string `json:"image_url"`
	PublishedAt string `json:"published_at"`
	Category   string `json:"category"`
}

type NewsResult struct {
	Articles      []NewsItem `json:"articles"`
	NextPageToken string     `json:"next_page_token"`
	HasMore       bool       `json:"has_more"`
	TotalResults  int        `json:"total_results"`
}

var validNewsCategories = map[string]bool{
	"top": true, "technology": true, "business": true, "sports": true,
	"entertainment": true, "health": true, "science": true, "politics": true, "world": true,
}

func ValidNewsCategory(cat string) bool {
	return validNewsCategories[cat]
}

var gnewsTopicMap = map[string]string{
	"top": "breaking-news", "technology": "technology", "business": "business",
	"sports": "sports", "entertainment": "entertainment", "health": "health",
	"science": "science", "politics": "nation", "world": "world",
}

type newsTool struct{}

func NewNewsTool() Tool { return &newsTool{} }

func (t *newsTool) Name() string        { return "news" }
func (t *newsTool) Description() string  { return "Tìm tin tức mới nhất theo chủ đề hoặc từ khóa" }
func (t *newsTool) Category() ToolCategory { return CategoryInformationRetrieval }
func (t *newsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"category": map[string]interface{}{
				"type":        "string",
				"description": "Danh mục tin: top, technology, business, sports, entertainment, health, science, politics, world",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Từ khóa tìm kiếm (tùy chọn)",
			},
		},
		"required": []string{},
	}
}
func (t *newsTool) Timeout() time.Duration        { return 5 * time.Second }
func (t *newsTool) TierLimit() map[string]int      { return map[string]int{"FREE": 15, "PRO": 60} }
func (t *newsTool) RequiresConfirmation() bool      { return false }

func (t *newsTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Category string `json:"category"`
		Query    string `json:"query"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &params); err != nil {
			return ToolResult{}, fmt.Errorf("tham số không hợp lệ: %v", err)
		}
	}

	category := strings.TrimSpace(strings.ToLower(params.Category))
	if category == "" {
		category = "top"
	}
	if !validNewsCategories[category] {
		return ToolResult{}, fmt.Errorf("danh mục không hợp lệ: %s", category)
	}

	query := strings.TrimSpace(params.Query)
	if len(query) > 200 {
		return ToolResult{}, fmt.Errorf("từ khóa tìm kiếm quá dài")
	}

	queryHash := HashQuery("news", category, query, "vi", "vn")
	data, err := CachedExecute(ctx, "news", queryHash, CacheScopeGlobal, "", newsCacheTTL, func() (json.RawMessage, error) {
		return fetchNews(ctx, category, query, "", "vi", "vn")
	})
	if err != nil {
		return ToolResult{}, err
	}

	richContent, err := json.Marshal(map[string]interface{}{
		"type":      "news",
		"data":      json.RawMessage(data),
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		richContent = nil
	}

	return ToolResult{
		JSONOutput:  data,
		RichContent: richContent,
	}, nil
}

func fetchNews(ctx context.Context, category, query, pageToken, language, country string) (json.RawMessage, error) {
	if key := config.Env.NewsDataAPIKey; key != "" {
		pCtx, pCancel := context.WithTimeout(ctx, newsProviderTimeout)
		data, err := fetchNewsData(pCtx, category, query, pageToken, language, country, key)
		pCancel()
		if err == nil {
			return data, nil
		}
	}

	if key := config.Env.GNewsAPIKey; key != "" {
		pCtx, pCancel := context.WithTimeout(ctx, newsProviderTimeout)
		data, err := fetchGNews(pCtx, category, query, language, country, key)
		pCancel()
		if err == nil {
			return data, nil
		}
	}

	pCtx2, pCancel2 := context.WithTimeout(ctx, newsProviderTimeout)
	data, err := fetchVnExpressRSS(pCtx2, category)
	pCancel2()
	if err == nil {
		return data, nil
	}

	pCtx3, pCancel3 := context.WithTimeout(ctx, newsProviderTimeout)
	data, err = fetchTuoiTreRSS(pCtx3, category)
	pCancel3()
	if err == nil {
		return data, nil
	}

	pCtx4, pCancel4 := context.WithTimeout(ctx, newsProviderTimeout)
	data, err = fetchThanhNienRSS(pCtx4, category)
	pCancel4()
	if err == nil {
		return data, nil
	}

	if key := config.Env.WorldNewsAPIKey; key != "" {
		pCtx5, pCancel5 := context.WithTimeout(ctx, newsProviderTimeout)
		data, err = fetchWorldNewsAPI(pCtx5, category, query, key)
		pCancel5()
		if err == nil {
			return data, nil
		}
	}

	return nil, fmt.Errorf("không có provider tin tức nào khả dụng")
}

func fetchNewsData(ctx context.Context, category, query, pageToken, language, country, apiKey string) (json.RawMessage, error) {
	params := url.Values{}
	params.Set("apikey", apiKey)
	params.Set("language", language)
	params.Set("country", country)
	if category != "" && category != "top" {
		params.Set("category", category)
	}
	if query != "" {
		params.Set("q", query)
	}
	if pageToken != "" {
		params.Set("page", pageToken)
	}

	reqURL := "https://newsdata.io/api/1/latest?" + params.Encode()
	body, err := httpGetJSON(ctx, reqURL)
	if err != nil {
		return nil, fmt.Errorf("NewsData.io: %w", err)
	}

	var resp struct {
		Status       string `json:"status"`
		TotalResults int    `json:"totalResults"`
		NextPage     string `json:"nextPage"`
		Results      []struct {
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Link        string   `json:"link"`
			SourceName  string   `json:"source_name"`
			ImageURL    string   `json:"image_url"`
			PubDate     string   `json:"pubDate"`
			Category    []string `json:"category"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("NewsData.io parse: %w", err)
	}

	articles := make([]NewsItem, 0, len(resp.Results))
	for _, r := range resp.Results {
		cat := category
		if len(r.Category) > 0 {
			cat = r.Category[0]
		}
		articles = append(articles, NewsItem{
			Title:       r.Title,
			Description: r.Description,
			Source:      r.SourceName,
			URL:        r.Link,
			ImageURL:   r.ImageURL,
			PublishedAt: r.PubDate,
			Category:   cat,
		})
	}

	result := NewsResult{
		Articles:      articles,
		NextPageToken: resp.NextPage,
		HasMore:       resp.NextPage != "",
		TotalResults:  resp.TotalResults,
	}
	return json.Marshal(result)
}

func fetchGNews(ctx context.Context, category, query, language, country, apiKey string) (json.RawMessage, error) {
	params := url.Values{}
	params.Set("token", apiKey)
	params.Set("lang", language)
	params.Set("country", country)
	params.Set("max", "10")

	var reqURL string
	if query != "" {
		params.Set("q", query)
		reqURL = "https://gnews.io/api/v4/search?" + params.Encode()
	} else {
		topic := gnewsTopicMap[category]
		if topic == "" {
			topic = "breaking-news"
		}
		params.Set("topic", topic)
		reqURL = "https://gnews.io/api/v4/top-headlines?" + params.Encode()
	}

	body, err := httpGetJSON(ctx, reqURL)
	if err != nil {
		return nil, fmt.Errorf("GNews.io: %w", err)
	}

	var resp struct {
		TotalArticles int `json:"totalArticles"`
		Articles      []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			URL         string `json:"url"`
			Image       string `json:"image"`
			PublishedAt string `json:"publishedAt"`
			Source      struct {
				Name string `json:"name"`
			} `json:"source"`
		} `json:"articles"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("GNews.io parse: %w", err)
	}

	articles := make([]NewsItem, 0, len(resp.Articles))
	for _, a := range resp.Articles {
		articles = append(articles, NewsItem{
			Title:       a.Title,
			Description: a.Description,
			Source:      a.Source.Name,
			URL:        a.URL,
			ImageURL:   a.Image,
			PublishedAt: a.PublishedAt,
			Category:   category,
		})
	}

	result := NewsResult{
		Articles:      articles,
		NextPageToken: "",
		HasMore:       false,
		TotalResults:  resp.TotalArticles,
	}
	return json.Marshal(result)
}

// FetchNewsPage is exported for use by the news pagination controller.
func FetchNewsPage(ctx context.Context, category, query, pageToken, language, country string) (json.RawMessage, error) {
	queryHash := HashQuery("news", "page", category, query, pageToken, language, country)
	return CachedExecute(ctx, "news", queryHash, CacheScopeGlobal, "", newsCacheTTL, func() (json.RawMessage, error) {
		return fetchNews(ctx, category, query, pageToken, language, country)
	})
}
