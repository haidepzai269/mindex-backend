package utils

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"strings"
	"time"

	"mindex-backend/config"
)

const (
	AnswerCacheTTLDoc    = 24 * time.Hour
	AnswerCacheTTLGlobal = 6 * time.Hour
)

type CachedAnswer struct {
	Answer  string                   `json:"answer"`
	Sources []map[string]interface{} `json:"sources"`
}

// NormalizeAnswerQuery chuẩn hoá câu hỏi để tạo cache key nhất quán:
// bỏ dấu tiếng Việt, lowercase, bỏ ký tự đặc biệt, chuẩn hoá khoảng trắng.
// Không sort từ (giữ nguyên thứ tự để "A là gì" ≠ "gì là A").
func NormalizeAnswerQuery(q string) string {
	q = RemoveVietnameseSigns(q)
	var b strings.Builder
	for _, r := range q {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// AnswerCacheKey tạo Redis key duy nhất cho câu trả lời dựa trên câu hỏi,
// phạm vi tài liệu (doc_id/collection_id/"global") và persona của user.
func AnswerCacheKey(question, scopeID, persona string) string {
	normalized := NormalizeAnswerQuery(question)
	raw := normalized + ":" + scopeID + ":" + persona
	sum := sha256.Sum256([]byte(raw))
	return "ai_answer:" + hex.EncodeToString(sum[:])
}

// GetAnswerCache lấy câu trả lời đã cache từ Redis.
func GetAnswerCache(ctx context.Context, key string) (*CachedAnswer, bool) {
	if config.RedisClient == nil {
		return nil, false
	}
	val, err := config.RedisClient.Get(ctx, key).Result()
	if err != nil {
		return nil, false
	}
	var cached CachedAnswer
	if err := json.Unmarshal([]byte(val), &cached); err != nil {
		log.Printf("⚠️ [AnswerCache] Unmarshal error key=%s: %v", key, err)
		return nil, false
	}
	return &cached, true
}

// SetAnswerCache lưu câu trả lời vào Redis với TTL cho trước.
func SetAnswerCache(ctx context.Context, key, answer string, sources []map[string]interface{}, ttl time.Duration) {
	if config.RedisClient == nil || strings.TrimSpace(answer) == "" {
		return
	}
	data, err := json.Marshal(CachedAnswer{Answer: answer, Sources: sources})
	if err != nil {
		log.Printf("⚠️ [AnswerCache] Marshal error: %v", err)
		return
	}
	if err := config.RedisClient.Set(ctx, key, string(data), ttl).Err(); err != nil {
		log.Printf("⚠️ [AnswerCache] Set error key=%s: %v", key, err)
		return
	}
	log.Printf("💾 [AnswerCache] Cached answer key=%s ttl=%v", key, ttl)
}
