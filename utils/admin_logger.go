package utils

import (
	"context"
	"log"
	"mindex-backend/config"
	"time"
)

type TokenUsageLog struct {
	UserID           *string
	DocumentID       *string
	SessionID        string
	Service          string // groq, gemini, huggingface, gemini_embed
	Operation        string // chat, summary, classify, rewrite, upload
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	LatencyMs        int
	KeyAlias         string
	Status           string // ok, error, timeout
	ErrorCode        string
}

func LogTokenUsage(entry TokenUsageLog) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := config.DB.Exec(ctx, `
			INSERT INTO token_usage_logs
			(user_id, document_id, session_id, service, operation,
			 prompt_tokens, completion_tokens, total_tokens,
			 latency_ms, api_key_alias, status, error_code)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			entry.UserID, entry.DocumentID, entry.SessionID,
			entry.Service, entry.Operation,
			entry.PromptTokens, entry.CompletionTokens, entry.TotalTokens,
			entry.LatencyMs, entry.KeyAlias, entry.Status, entry.ErrorCode)

		if err != nil {
			log.Printf("❌ [AdminLogger] Failed to insert token log: %v", err)
		}
	}()
}
