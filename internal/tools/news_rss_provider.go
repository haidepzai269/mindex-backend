package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

)

type rssResponse struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

var htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

func stripHTMLTags(s string) string {
	return strings.TrimSpace(htmlTagRegex.ReplaceAllString(s, ""))
}

var vnexpressCategoryMap = map[string]string{
	"top":           "tin-moi-nhat",
	"technology":    "so-hoa",
	"business":      "kinh-doanh",
	"sports":        "the-thao",
	"entertainment": "giai-tri",
	"health":        "suc-khoe",
	"science":       "so-hoa",
	"politics":      "thoi-su",
	"world":         "the-gioi",
}

var tuoitreCategoryMap = map[string]string{
	"top":           "tin-moi-nhat",
	"technology":    "nhip-song-so",
	"business":      "kinh-doanh",
	"sports":        "the-thao",
	"entertainment": "giai-tri",
	"health":        "suc-khoe",
	"science":       "nhip-song-so",
	"politics":      "thoi-su",
	"world":         "the-gioi",
}

var thanhnienCategoryMap = map[string]string{
	"top":           "home",
	"technology":    "cong-nghe",
	"business":      "tai-chinh-kinh-doanh",
	"sports":        "the-thao",
	"entertainment": "giai-tri",
	"health":        "suc-khoe",
	"science":       "cong-nghe",
	"politics":      "thoi-su",
	"world":         "the-gioi",
}

func fetchRSS(ctx context.Context, feedURL, sourceName, category string) (json.RawMessage, error) {
	body, err := httpGetJSON(ctx, feedURL)
	if err != nil {
		return nil, fmt.Errorf("%s RSS: %w", sourceName, err)
	}

	var rss rssResponse
	if err := xml.Unmarshal(body, &rss); err != nil {
		return nil, fmt.Errorf("%s RSS parse: %w", sourceName, err)
	}

	articles := make([]NewsItem, 0, len(rss.Channel.Items))
	for _, item := range rss.Channel.Items {
		if item.Title == "" {
			continue
		}
		articles = append(articles, NewsItem{
			Title:       strings.TrimSpace(item.Title),
			Description: stripHTMLTags(item.Description),
			Source:      sourceName,
			URL:         strings.TrimSpace(item.Link),
			PublishedAt: item.PubDate,
			Category:    category,
		})
	}

	if len(articles) == 0 {
		return nil, fmt.Errorf("%s RSS: no articles found", sourceName)
	}

	result := NewsResult{
		Articles:      articles,
		NextPageToken: "",
		HasMore:       false,
		TotalResults:  len(articles),
	}
	return json.Marshal(result)
}

func fetchVnExpressRSS(ctx context.Context, category string) (json.RawMessage, error) {
	slug := vnexpressCategoryMap[category]
	if slug == "" {
		slug = "tin-moi-nhat"
	}
	feedURL := fmt.Sprintf("https://vnexpress.net/rss/%s.rss", slug)
	return fetchRSS(ctx, feedURL, "VnExpress", category)
}

func fetchTuoiTreRSS(ctx context.Context, category string) (json.RawMessage, error) {
	slug := tuoitreCategoryMap[category]
	if slug == "" {
		slug = "tin-moi-nhat"
	}
	feedURL := fmt.Sprintf("https://tuoitre.vn/rss/%s.rss", slug)
	return fetchRSS(ctx, feedURL, "Tuổi Trẻ", category)
}

func fetchThanhNienRSS(ctx context.Context, category string) (json.RawMessage, error) {
	slug := thanhnienCategoryMap[category]
	if slug == "" {
		slug = "home"
	}
	feedURL := fmt.Sprintf("https://thanhnien.vn/rss/%s.rss", slug)
	return fetchRSS(ctx, feedURL, "Thanh Niên", category)
}

func fetchWorldNewsAPI(ctx context.Context, category, query, apiKey string) (json.RawMessage, error) {
	params := url.Values{}
	params.Set("source-countries", "vn")
	params.Set("language", "vi")
	params.Set("number", "10")
	params.Set("sort", "publish-time")
	params.Set("sort-direction", "DESC")
	if query != "" {
		params.Set("text", query)
	}

	reqURL := "https://api.worldnewsapi.com/search-news?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("WorldNewsAPI: %w", err)
	}
	req.Header.Set("api-key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("WorldNewsAPI: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("WorldNewsAPI HTTP %d: %s", resp.StatusCode, string(errBody))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("WorldNewsAPI read: %w", err)
	}

	var apiResp struct {
		News []struct {
			Title       string `json:"title"`
			Text        string `json:"text"`
			URL         string `json:"url"`
			Image       string `json:"image"`
			PublishDate string `json:"publish_date"`
		} `json:"news"`
		Available int `json:"available"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("WorldNewsAPI parse: %w", err)
	}

	articles := make([]NewsItem, 0, len(apiResp.News))
	for _, n := range apiResp.News {
		desc := n.Text
		if len(desc) > 200 {
			desc = desc[:200] + "..."
		}
		articles = append(articles, NewsItem{
			Title:       n.Title,
			Description: desc,
			Source:      "WorldNewsAPI",
			URL:         n.URL,
			ImageURL:    n.Image,
			PublishedAt: n.PublishDate,
			Category:    category,
		})
	}

	if len(articles) == 0 {
		return nil, fmt.Errorf("WorldNewsAPI: no articles found")
	}

	result := NewsResult{
		Articles:      articles,
		NextPageToken: "",
		HasMore:       false,
		TotalResults:  apiResp.Available,
	}
	return json.Marshal(result)
}

