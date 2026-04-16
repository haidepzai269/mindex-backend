package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils/quota"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type KeyStatus struct {
	Alias             string `json:"alias"`
	Service           string `json:"service"`
	LimitTokens       int64  `json:"limit_tokens"`
	RemainingTokens   int64  `json:"remaining_tokens"`
	LimitRequests     int64  `json:"limit_requests"`
	RemainingRequests int64  `json:"remaining_requests"`
	ResetAt           int64  `json:"reset_at"`
	LastUpdated       int64  `json:"last_updated"`
}

type ApiKeyPool struct {
	keys    []string
	counter uint64
	name    string
}

var (
	GeminiChatPool  *ApiKeyPool
	GeminiEmbedPool *ApiKeyPool
	GeminiPool      *ApiKeyPool // Legacy alias
	GroqPool        *ApiKeyPool
	CerebrasPool    *ApiKeyPool
	MistralPool     *ApiKeyPool
	OpenRouterPool  *ApiKeyPool
	HFPool          *ApiKeyPool
	NineRouterPool  *ApiKeyPool
	NineRouterChatPool *ApiKeyPool
)

// NewApiKeyPool khởi tạo một pool cụ thể
func NewApiKeyPool(name string, keys []string) *ApiKeyPool {
	return &ApiKeyPool{
		name:    name,
		keys:    keys,
		counter: 0,
	}
}

// GetKey returns the actual key and its alias
func (p *ApiKeyPool) GetKey() (string, string) {
	if p == nil || len(p.keys) == 0 {
		return "", ""
	}
	idx := atomic.AddUint64(&p.counter, 1)
	index := (idx - 1) % uint64(len(p.keys))
	return p.keys[index], fmt.Sprintf("%s_key_%d", p.name, index+1)
}

// UpdateKeyStatusFromHeaders phân tích header quota và lưu vào Redis
func UpdateKeyStatusFromHeaders(service, alias, apiKey string, headers http.Header) {
	if config.RedisClient == nil {
		return
	}

	// Cập nhật vào hệ thống Quota Tracker mới (Lưu DB + Redis)
	if quota.GlobalTracker != nil && apiKey != "" {
		quota.GlobalTracker.ParseHeaders(apiKey, headers, 0)
	}

	status := KeyStatus{
		Alias:       alias,
		Service:     service,
		LastUpdated: time.Now().Unix(),
	}

	hasData := false

	// Groq Headers
	if lt := headers.Get("x-ratelimit-limit-tokens"); lt != "" {
		status.LimitTokens, _ = strconv.ParseInt(lt, 10, 64)
		hasData = true
	}
	if rt := headers.Get("x-ratelimit-remaining-tokens"); rt != "" {
		status.RemainingTokens, _ = strconv.ParseInt(rt, 10, 64)
		hasData = true
	}
	if lr := headers.Get("x-ratelimit-limit-requests"); lr != "" {
		status.LimitRequests, _ = strconv.ParseInt(lr, 10, 64)
		hasData = true
	}
	if rr := headers.Get("x-ratelimit-remaining-requests"); rr != "" {
		status.RemainingRequests, _ = strconv.ParseInt(rr, 10, 64)
		hasData = true
	}
	if rs := headers.Get("x-ratelimit-reset-tokens"); rs != "" {
		// x-ratelimit-reset-tokens: 60ms, 1s, etc.
		if dur, err := time.ParseDuration(strings.ReplaceAll(rs, "ms", "ms")); err == nil {
			status.ResetAt = time.Now().Add(dur).Unix()
		}
	}

	// HuggingFace Headers
	if lt := headers.Get("x-rate-limit-limit"); lt != "" {
		status.LimitRequests, _ = strconv.ParseInt(lt, 10, 64)
		hasData = true
	}
	if rt := headers.Get("x-rate-limit-remaining"); rt != "" {
		status.RemainingRequests, _ = strconv.ParseInt(rt, 10, 64)
		hasData = true
	}

	if !hasData {
		return
	}

	// Lưu vào Redis
	data, _ := json.Marshal(status)
	redisKey := fmt.Sprintf("key_status:%s:%s", service, alias)
	config.RedisClient.Set(context.Background(), redisKey, data, 24*time.Hour)
	
	// Lưu alias vào một set để dễ dàng lấy danh sách
	config.RedisClient.SAdd(context.Background(), "active_api_keys", redisKey)

	// Publish cho SSE real-time
	config.RedisClient.Publish(context.Background(), "api_key_updates", string(data))
}

// QuotaTransport đánh chặn HTTP Response để lấy header
type QuotaTransport struct {
	Base    http.RoundTripper
	Service string
	Alias   string
	ApiKey  string
}

func (t *QuotaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Nếu là Gemini, tự động đính kèm API Key vào URL nếu chưa có
	if t.Service == "gemini" && t.ApiKey != "" {
		q := req.URL.Query()
		if q.Get("key") == "" {
			q.Set("key", t.ApiKey)
			req.URL.RawQuery = q.Encode()
		}
	}

	resp, err := t.Base.RoundTrip(req)
	// Cập nhật vào hệ thống Quota Tracker mới (Lưu DB + Redis)
	if quota.GlobalTracker != nil {
		quota.GlobalTracker.ParseHeaders(t.ApiKey, resp.Header, 0)
	}

	return resp, err
}

func NewQuotaHttpClient(service, alias, apiKey string) *http.Client {
	return &http.Client{
		Transport: &QuotaTransport{
			Base:    http.DefaultTransport,
			Service: service,
			Alias:   alias,
			ApiKey:  apiKey,
		},
		Timeout: 180 * time.Second,
	}
}

// EmbedWithRetry thực hiện lời gọi API Embedding thực tế với cơ chế xoay vòng Key
func (p *ApiKeyPool) EmbedWithRetry(text string, callGeminiAPI func(string, string, string) ([]float32, error)) ([]float32, error) {
	var lastErr error
	for i := 0; i < len(p.keys); i++ {
		key, alias := p.GetKey()
		vec, err := callGeminiAPI(key, alias, text)

		if err == nil {
			log.Printf("✅ Embedding thành công sử dụng %s", alias)
			return vec, nil
		}

		lastErr = err
		log.Printf("⚠️ Lỗi Embedding với %s: %v. Đang thử key tiếp theo...", alias, err)
	}
	return nil, fmt.Errorf("tất cả API keys trong pool %s đã thất bại: %v", p.name, lastErr)
}
