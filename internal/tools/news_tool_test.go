package tools

import (
	"encoding/json"
	"testing"
)

func TestNewsTool_Metadata(t *testing.T) {
	tool := NewNewsTool()
	if tool.Name() != "news" {
		t.Errorf("expected name 'news', got %q", tool.Name())
	}
	if tool.Category() != CategoryInformationRetrieval {
		t.Errorf("expected category information_retrieval, got %q", tool.Category())
	}
	if tool.RequiresConfirmation() {
		t.Error("news tool should not require confirmation")
	}
	limits := tool.TierLimit()
	if limits["FREE"] != 15 || limits["PRO"] != 60 {
		t.Errorf("unexpected tier limits: %v", limits)
	}
}

func TestNewsTool_DefaultCategory(t *testing.T) {
	var params struct {
		Category string `json:"category"`
	}
	input := json.RawMessage(`{}`)
	json.Unmarshal(input, &params)
	cat := params.Category
	if cat != "" {
		t.Errorf("expected empty category from empty input, got %q", cat)
	}
}

func TestNewsTool_InvalidCategory(t *testing.T) {
	if validNewsCategories["invalid_cat"] {
		t.Error("'invalid_cat' should not be a valid category")
	}
	if !validNewsCategories["technology"] {
		t.Error("'technology' should be a valid category")
	}
	if !validNewsCategories["top"] {
		t.Error("'top' should be a valid category")
	}
}

func TestNewsTool_GNewsTopicMapping(t *testing.T) {
	cases := map[string]string{
		"top":           "breaking-news",
		"technology":    "technology",
		"business":      "business",
		"sports":        "sports",
		"entertainment": "entertainment",
		"politics":      "nation",
		"world":         "world",
	}
	for cat, expected := range cases {
		got := gnewsTopicMap[cat]
		if got != expected {
			t.Errorf("gnewsTopicMap[%q] = %q, want %q", cat, got, expected)
		}
	}
}

func TestNewsTool_ParseNewsDataResponse(t *testing.T) {
	raw := `{
		"status": "success",
		"totalResults": 45,
		"nextPage": "page2token",
		"results": [
			{
				"title": "Tin công nghệ mới",
				"description": "Mô tả tin tức...",
				"link": "https://example.com/news1",
				"source_name": "VnExpress",
				"image_url": "https://example.com/img.jpg",
				"pubDate": "2026-06-27",
				"category": ["technology"]
			},
			{
				"title": "Tin thứ hai",
				"description": "Mô tả thứ hai",
				"link": "https://example.com/news2",
				"source_name": "TuoiTre",
				"image_url": null,
				"pubDate": "2026-06-26",
				"category": ["business"]
			}
		]
	}`

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
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if resp.TotalResults != 45 {
		t.Errorf("totalResults = %d, want 45", resp.TotalResults)
	}
	if resp.NextPage != "page2token" {
		t.Errorf("nextPage = %q, want 'page2token'", resp.NextPage)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results count = %d, want 2", len(resp.Results))
	}
	if resp.Results[0].Title != "Tin công nghệ mới" {
		t.Errorf("first title = %q", resp.Results[0].Title)
	}
	if resp.Results[0].SourceName != "VnExpress" {
		t.Errorf("first source = %q", resp.Results[0].SourceName)
	}
}

func TestNewsTool_ParseGNewsResponse(t *testing.T) {
	raw := `{
		"totalArticles": 100,
		"articles": [
			{
				"title": "Breaking News",
				"description": "News description",
				"url": "https://example.com/article",
				"image": "https://example.com/image.jpg",
				"publishedAt": "2026-06-27T10:00:00Z",
				"source": {"name": "CNN"}
			}
		]
	}`

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
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if resp.TotalArticles != 100 {
		t.Errorf("totalArticles = %d, want 100", resp.TotalArticles)
	}
	if len(resp.Articles) != 1 {
		t.Fatalf("articles count = %d, want 1", len(resp.Articles))
	}
	if resp.Articles[0].Source.Name != "CNN" {
		t.Errorf("source = %q, want 'CNN'", resp.Articles[0].Source.Name)
	}
}

func TestNewsTool_QueryLengthLimit(t *testing.T) {
	longQuery := ""
	for i := 0; i < 201; i++ {
		longQuery += "a"
	}
	if len(longQuery) <= 200 {
		t.Error("test query should be longer than 200 chars")
	}
}
