package quota

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"mindex-backend/config"
)

// Provider constants
const (
	ProviderGroq        = "groq"
	ProviderGemini      = "gemini"
	ProviderCerebras    = "cerebras"
	ProviderMistral     = "mistral"
	ProviderOpenRouter  = "openrouter"
	ProviderHuggingFace = "huggingface"
)

// Gemini Model Limits (Dựa trên ảnh Google AI Studio của người dùng)
const (
	GeminiFlashRPM    = 5
	GeminiFlashTPM    = 250000
	GeminiLiteRPM     = 15
	GeminiLiteTPM     = 1000000
	GeminiEmbeddingRPM = 100
	GeminiEmbeddingTPM = 30000
)

// KeyUsage tracks a single API key's usage
type KeyUsage struct {
	KeyID       string    `json:"key_id"`       // masked key, e.g. "sk-...ab12"
	ApiKey      string    `json:"-"`            // actual key (hidden from JSON)
	Provider    string    `json:"provider"`
	AccountNote string    `json:"account_note"`

	// Request limits (Real-time from headers/Redis)
	RPMRemaining int64 `json:"rpm_remaining"`
	RPMLimit     int64 `json:"rpm_limit"`
	TPMRemaining int64 `json:"tpm_remaining"`
	TPMLimit     int64 `json:"tpm_limit"`
	ResetAt      time.Time `json:"reset_at"`

	// Cumulative usage (From DB)
	RPDUsed           int64 `json:"rpd_used"`
	MonthlyTokenUsed  int64 `json:"monthly_token_used"`

	// Status
	IsRateLimited bool      `json:"is_rate_limited"`
	LastUsed      time.Time `json:"last_used"`
	LastError     string    `json:"last_error,omitempty"`

	mu sync.Mutex
}

type Tracker struct {
	keys map[string]*KeyUsage // apiKey -> usage
	mu   sync.RWMutex
}

var GlobalTracker *Tracker

func InitTracker() {
	GlobalTracker = &Tracker{
		keys: make(map[string]*KeyUsage),
	}
	go GlobalTracker.syncLoop()
	go GlobalTracker.quotaRefresherLoop() // Khởi chạy tiến trình hồi phục hạn ngạch tự động
}

// quotaRefresherLoop tự động hồi phục RPM/TPM cho các provider không có header (như Gemini) mỗi 60s
func (t *Tracker) quotaRefresherLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		t.mu.RLock()
		for _, u := range t.keys {
			u.mu.Lock()
			// Chỉ tự động hồi phục nếu key có thiết lập Limit (do chúng ta ước tính)
			if u.RPMLimit > 0 {
				u.RPMRemaining = u.RPMLimit
				u.TPMRemaining = u.TPMLimit
				u.ResetAt = time.Now().Add(60 * time.Second)
				t.saveToRedis(u)
			}
			u.mu.Unlock()
		}
		t.mu.RUnlock()
	}
}

// MaskKey returns a masked version of the API key
func MaskKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// RegisterKey adds an API key to track and loads initial data from DB/Redis
func (t *Tracker) RegisterKey(apiKey, provider, accountNote string) {
	keyID := MaskKey(apiKey)
	
	t.mu.Lock()
	usage := &KeyUsage{
		KeyID:       keyID,
		ApiKey:      apiKey,
		Provider:    provider,
		AccountNote: accountNote,
		LastUsed:    time.Now(),
		ResetAt:     time.Now().Add(1 * time.Minute),
	}

	// Thiết lập hạn mức mặc định cho Gemini dựa trên phân loại Key (AccountNote)
	if provider == ProviderGemini {
		if strings.Contains(accountNote, "chat") {
			// Mặc định cho Chat hiện tại là Flash Lite theo Orchestrator
			usage.RPMLimit = GeminiLiteRPM
			usage.TPMLimit = GeminiLiteTPM
		} else if strings.Contains(accountNote, "embed") {
			usage.RPMLimit = GeminiEmbeddingRPM
			usage.TPMLimit = GeminiEmbeddingTPM
		} else {
			// Fallback cho model Flash tiêu chuẩn
			usage.RPMLimit = GeminiFlashRPM
			usage.TPMLimit = GeminiFlashTPM
		}
		usage.RPMRemaining = usage.RPMLimit
		usage.TPMRemaining = usage.TPMLimit
	}

	t.keys[apiKey] = usage
	t.mu.Unlock()

	// Load existing usage from DB
	ctx := context.Background()
	var rpdUsed, monthlyUsed int64
	var lastUsed time.Time
	
	err := config.DB.QueryRow(ctx, 
		"SELECT rpd_used, monthly_token_used, last_used FROM api_key_quotas WHERE api_key_id = $1 AND provider = $2",
		keyID, provider).Scan(&rpdUsed, &monthlyUsed, &lastUsed)
	
	if err != nil {
		// New key, create entry in DB
		_, _ = config.DB.Exec(ctx, 
			"INSERT INTO api_key_quotas (api_key_id, provider) VALUES ($1, $2) ON CONFLICT DO NOTHING",
			keyID, provider)
	} else {
		usage.mu.Lock()
		usage.RPDUsed = rpdUsed
		usage.MonthlyTokenUsed = monthlyUsed
		usage.LastUsed = lastUsed
		usage.mu.Unlock()
	}

	// Load real-time limits from Redis if available
	if config.RedisClient != nil {
		redisKey := fmt.Sprintf("quota:v1:%s:%s", keyID, provider)
		data, err := config.RedisClient.HGetAll(ctx, redisKey).Result()
		if err == nil && len(data) > 0 {
			usage.mu.Lock()
			// Nếu trong Redis có giá trị limit > 0 thì dùng nó, nếu không dùng giá trị mặc định đã xét ở trên
			if rlim, _ := strconv.ParseInt(data["rpm_limit"], 10, 64); rlim > 0 {
				usage.RPMLimit = rlim
			}
			if tlim, _ := strconv.ParseInt(data["tpm_limit"], 10, 64); tlim > 0 {
				usage.TPMLimit = tlim
			}
			usage.RPMRemaining, _ = strconv.ParseInt(data["rpm_rem"], 10, 64)
			usage.TPMRemaining, _ = strconv.ParseInt(data["tpm_rem"], 10, 64)
			if resetStr, ok := data["reset_at"]; ok {
				if n, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
					usage.ResetAt = time.Unix(n, 0)
				}
			}
			usage.mu.Unlock()
		}
	}
}

// ParseHeaders reads rate limit info from HTTP response headers
func (t *Tracker) ParseHeaders(apiKey string, headers http.Header, tokensUsed int64) {
	t.mu.RLock()
	usage, ok := t.keys[apiKey]
	t.mu.RUnlock()
	if !ok {
		return
	}

	usage.mu.Lock()
	defer usage.mu.Unlock()

	// Parse remaining requests
	if v := headers.Get("x-ratelimit-remaining-requests"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			usage.RPMRemaining = n
		}
	}
    if v := headers.Get("x-ratelimit-limit-requests"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			usage.RPMLimit = n
		}
	}

	// Parse remaining tokens
	if v := headers.Get("x-ratelimit-remaining-tokens"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			usage.TPMRemaining = n
		}
	}
    if v := headers.Get("x-ratelimit-limit-tokens"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			usage.TPMLimit = n
		}
	}

	// Parse reset time (Groq: "1m30s", Others: Unix timestamp or seconds)
	if v := headers.Get("x-ratelimit-reset-requests"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			usage.ResetAt = time.Now().Add(d)
		} else if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			// Some APIs return seconds until reset, some return unix timestamp
			if n > 2000000000 { // Likely unix timestamp in ms or far future
                usage.ResetAt = time.Unix(n/1000, 0)
            } else if n > 1000000000 { // Likely unix timestamp
                usage.ResetAt = time.Unix(n, 0)
            } else {
                usage.ResetAt = time.Now().Add(time.Duration(n) * time.Second)
            }
		}
	}

	usage.RPDUsed++
	usage.MonthlyTokenUsed += tokensUsed
	usage.LastUsed = time.Now()
	usage.IsRateLimited = false

	// Update Redis immediately for real-time consistency
	t.saveToRedis(usage)
}

// MarkRateLimited marks a key as rate limited (got 429)
func (t *Tracker) MarkRateLimited(apiKey string, err error) {
	t.mu.RLock()
	usage, ok := t.keys[apiKey]
	t.mu.RUnlock()
	if !ok {
		return
	}

	usage.mu.Lock()
	usage.IsRateLimited = true
	usage.LastError = err.Error()
	usage.mu.Unlock()
	
	t.saveToRedis(usage)
}

// RecordCall manually increments usage counters và trừ dần quota ước tính
func (t *Tracker) RecordCall(apiKey string, tokensUsed int64) {
	t.mu.RLock()
	usage, ok := t.keys[apiKey]
	t.mu.RUnlock()
	if !ok {
		return
	}

	usage.mu.Lock()
	usage.RPDUsed++
	usage.MonthlyTokenUsed += tokensUsed
	usage.LastUsed = time.Now()

	// Tự động trừ dần quota nếu không có header trả về (như Gemini)
	if usage.RPMRemaining > 0 {
		usage.RPMRemaining--
	}
	if usage.TPMRemaining > 0 && tokensUsed > 0 {
		usage.TPMRemaining -= tokensUsed
		if usage.TPMRemaining < 0 {
			usage.TPMRemaining = 0
		}
	}
	
	usage.mu.Unlock()

	t.saveToRedis(usage)
}

func (t *Tracker) saveToRedis(u *KeyUsage) {
	if config.RedisClient == nil {
		return
	}
	ctx := context.Background()
	redisKey := fmt.Sprintf("quota:v1:%s:%s", u.KeyID, u.Provider)
	
	config.RedisClient.HSet(ctx, redisKey, map[string]interface{}{
		"rpm_rem":      u.RPMRemaining,
		"rpm_limit":    u.RPMLimit,
		"tpm_rem":      u.TPMRemaining,
		"tpm_limit":    u.TPMLimit,
		"reset_at":     u.ResetAt.Unix(),
		"rpd_used":     u.RPDUsed,
		"monthly_used": u.MonthlyTokenUsed,
		"is_limited":   u.IsRateLimited,
		"last_err":     u.LastError,
	})
	// Set TTL to 1 day to clean up old keys
	config.RedisClient.Expire(ctx, redisKey, 24*time.Hour)
}

// syncLoop periodically saves memory data to Database
func (t *Tracker) syncLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		t.SyncToDB()
	}
}

func (t *Tracker) SyncToDB() {
	t.mu.RLock()
	snap := make([]KeyUsage, 0, len(t.keys))
	for _, u := range t.keys {
		u.mu.Lock()
		snap = append(snap, *u)
		u.mu.Unlock()
	}
	t.mu.RUnlock()

	ctx := context.Background()
	for _, u := range snap {
		_, err := config.DB.Exec(ctx, `
			UPDATE api_key_quotas 
			SET rpd_used = $1, monthly_token_used = $2, last_used = $3, updated_at = NOW()
			WHERE api_key_id = $4 AND provider = $5
		`, u.RPDUsed, u.MonthlyTokenUsed, u.LastUsed, u.KeyID, u.Provider)
		
		if err != nil {
			log.Printf("❌ Error syncing quota to DB for %s: %v", u.KeyID, err)
		}
	}
}

// GetAllUsage returns a snapshot for the UI
func (t *Tracker) GetAllUsage() []KeyUsage {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]KeyUsage, 0, len(t.keys))
	for _, u := range t.keys {
		u.mu.Lock()
		result = append(result, *u)
		u.mu.Unlock()
	}
	return result
}

// GetProviderSummary aggregates usage by provider
func (t *Tracker) GetProviderSummary() map[string]*ProviderSummary {
	all := t.GetAllUsage()
	summaries := make(map[string]*ProviderSummary)

	for _, u := range all {
		if _, ok := summaries[u.Provider]; !ok {
			summaries[u.Provider] = &ProviderSummary{
				Provider: u.Provider,
				Keys:     []KeyUsage{},
			}
		}
		s := summaries[u.Provider]
		s.TotalKeys++
		s.TotalRPDUsed += u.RPDUsed
		s.TotalMonthlyTokenUsed += u.MonthlyTokenUsed
		if u.IsRateLimited {
			s.RateLimitedKeys++
		}
		s.Keys = append(s.Keys, u)
	}
	return summaries
}

type ProviderSummary struct {
	Provider               string     `json:"provider"`
	TotalKeys              int        `json:"total_keys"`
	RateLimitedKeys        int        `json:"rate_limited_keys"`
	TotalRPDUsed           int64      `json:"total_rpd_used"`
	TotalMonthlyTokenUsed  int64      `json:"total_monthly_token_used"`
	Keys                   []KeyUsage `json:"keys"`
}

// HTTPHandler returns a handler for /api/v1/admin/quota endpoint
func (t *Tracker) HTTPHandler() func(c *context.Context) {
    // Note: Project uses Gin, so handler should be gin.HandlerFunc
    // I will implement it in the routes/admin.go instead to avoid circular dependencies
    return nil
}
