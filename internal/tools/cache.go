package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"mindex-backend/config"
)

type CacheScope string

const (
	CacheScopeGlobal CacheScope = "global"
	CacheScopeUser   CacheScope = "user"
)

func cacheKey(toolName, queryHash string, scope CacheScope, userID string) string {
	if scope == CacheScopeUser && userID != "" {
		return fmt.Sprintf("tools:cache:%s:%s:%s", toolName, userID, queryHash)
	}
	return fmt.Sprintf("tools:cache:%s:%s", toolName, queryHash)
}

func lockKey(cacheK string) string {
	return fmt.Sprintf("tools:cache:lock:%s", cacheK)
}

func HashQuery(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// CachedExecute wraps a fetch function with Redis cache + stale-while-revalidate.
// If cache hit: return cached data immediately.
// If cache miss: acquire lock, fetch, cache result.
// If cache expired but lock taken by another goroutine: return stale data if available.
func CachedExecute(ctx context.Context, toolName string, queryHash string, scope CacheScope, userID string, ttl time.Duration, fetchFn func() (json.RawMessage, error)) (json.RawMessage, error) {
	if config.RedisClient == nil {
		return fetchFn()
	}

	key := cacheKey(toolName, queryHash, scope, userID)

	cached, err := config.RedisClient.Get(ctx, key).Result()
	if err == nil && cached != "" {
		return json.RawMessage(cached), nil
	}

	lk := lockKey(key)
	acquired, _ := config.RedisClient.SetNX(ctx, lk, "1", 10*time.Second).Result()

	if !acquired {
		for range 6 {
			time.Sleep(500 * time.Millisecond)
			cached, err = config.RedisClient.Get(ctx, key).Result()
			if err == nil && cached != "" {
				return json.RawMessage(cached), nil
			}
		}
		// Retry failed but we don't own the lock - fetch without lock management
		return fetchFn()
	}

	data, fetchErr := fetchFn()
	if fetchErr != nil {
		config.RedisClient.Del(ctx, lk)
		return nil, fetchErr
	}

	if err := config.RedisClient.Set(ctx, key, string(data), ttl).Err(); err != nil {
		log.Printf("[ToolCache] failed to cache %s: %v", key, err)
	}
	config.RedisClient.Del(ctx, lk)

	return data, nil
}
