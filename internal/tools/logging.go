package tools

import (
	"encoding/json"
	"log"

	"mindex-backend/config"
)

// ToolCallRecord is one tool invocation, persisted to tool_calls for
// monitoring (FR-006) and SC-003/SC-004 measurement.
type ToolCallRecord struct {
	ToolName     string
	UserID       string
	SessionID    string
	Input        json.RawMessage
	Output       json.RawMessage
	DurationMs   int
	Status       string // success | error | timeout
	ErrorMessage string
	RichContent  json.RawMessage
}

// LogToolCall writes one row per tool execution. Failures to write are
// logged but never block the chat response (logging is best-effort).
func LogToolCall(rec ToolCallRecord) {
	if config.DB == nil {
		return
	}
	var sessionID interface{}
	if rec.SessionID != "" {
		sessionID = rec.SessionID
	}
	var errMsg interface{}
	if rec.ErrorMessage != "" {
		errMsg = rec.ErrorMessage
	}
	_, err := config.DB.Exec(config.Ctx, `
		INSERT INTO tool_calls (tool_name, user_id, session_id, input, output, duration_ms, status, error_message)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		rec.ToolName, rec.UserID, sessionID, rec.Input, rec.Output, rec.DurationMs, rec.Status, errMsg)
	if err != nil {
		log.Printf("⚠️ [Tools] LogToolCall failed for %s: %v", rec.ToolName, err)
	}
}
