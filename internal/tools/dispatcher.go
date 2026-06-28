package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"mindex-backend/config"
	"mindex-backend/utils"
)

// defaultMaxDepth bounds the agentic tool-calling loop (FR-015, Edge Case:
// "vòng lặp tool A gọi tool B gọi tool A").
const defaultMaxDepth = 10

// PendingConfirmation is returned instead of executing a side-effect tool
// (FR-009, SC-008). The caller (chat layer) must surface a confirmation
// prompt to the user; execution only happens via a follow-up confirm call
// using PendingID.
type PendingConfirmation struct {
	PendingID string
	ToolName  string
	Input     json.RawMessage
}

// DispatchResult is what one Dispatcher.Run call produces.
type DispatchResult struct {
	FinalAnswer  string
	ToolCalls    []ToolCallRecord
	Pending      *PendingConfirmation
	RichContent  json.RawMessage   // first rich content (backward compat)
	RichContents []json.RawMessage // all rich content items for multi-tool queries
}

// chatWithToolsFunc matches utils.AI.ChatNonStreamWithTools's signature.
// Abstracted into a field (rather than calling the package-level utils.AI
// singleton directly) so the multi-step loop, maxDepth termination, and
// partial-success behavior (US10) can be unit-tested with a stub instead of
// requiring a live LLM provider.
type chatWithToolsFunc func(service utils.ServiceType, messages []utils.ChatMessage, toolSchemas []utils.ToolSchema) (string, []utils.ToolCallRequest, utils.ProviderType, error)

// Dispatcher runs the function-calling loop: ask the LLM with tool schemas,
// execute any requested tools, feed results back, repeat until the LLM stops
// requesting tools or maxDepth is reached.
type Dispatcher struct {
	Registry *Registry
	chatFunc chatWithToolsFunc
}

func NewDispatcher(r *Registry) *Dispatcher {
	return &Dispatcher{Registry: r, chatFunc: utils.AI.ChatNonStreamWithTools}
}

// Run executes the loop. maxDepth <= 0 falls back to defaultMaxDepth.
func (d *Dispatcher) Run(ctx context.Context, execCtx ToolExecutionContext, messages []utils.ChatMessage, maxDepth int) (DispatchResult, error) {
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}

	schemas := d.toolSchemas()
	conversation := append([]utils.ChatMessage{}, messages...)
	var records []ToolCallRecord
	var lastAnswer string

	for depth := 0; depth < maxDepth; depth++ {
		answer, calls, _, err := d.chatFunc(utils.ServiceChat, conversation, schemas)
		if err != nil {
			// If at least one tool already ran successfully in this Run() call,
			// degrade gracefully instead of dropping everything: the model's
			// follow-up round failed (e.g. transient provider outage), but the
			// tool result itself is still useful to the user (Edge Case:
			// "Tool gọi bị lỗi... AI vẫn trả lời phần có thể trả lời được",
			// FR-005). A first-round failure with no tool results yet is a
			// genuine total-provider-failure and is still propagated as an
			// error, matching pre-existing behavior for non-tool chat calls.
			if len(records) > 0 {
				rcs := collectRichContents(records)
				return DispatchResult{FinalAnswer: buildFallbackAnswer(records), ToolCalls: records, RichContent: firstRichContent(records), RichContents: rcs}, nil
			}
			return DispatchResult{FinalAnswer: lastAnswer, ToolCalls: records}, err
		}
		lastAnswer = answer

		if len(calls) == 0 {
			rcs := collectRichContents(records)
			return DispatchResult{FinalAnswer: answer, ToolCalls: records, RichContent: firstRichContent(records), RichContents: rcs}, nil
		}

		conversation = append(conversation, utils.ChatMessage{Role: "assistant", Content: answer, ToolCalls: calls})

		for _, call := range calls {
			tool, ok := d.Registry.Get(call.Name)
			if !ok {
				rec := ToolCallRecord{
					ToolName:     call.Name,
					UserID:       execCtx.UserID,
					SessionID:    execCtx.SessionID,
					Status:       "error",
					ErrorMessage: fmt.Sprintf("tool %q không tồn tại trong registry", call.Name),
				}
				records = append(records, rec)
				conversation = append(conversation, toolResultMessage(call.ID, rec))
				continue
			}

			if tool.RequiresConfirmation() {
				pendingID := uuid.New().String()
				input := argumentsToRawMessage(call.Arguments)
				storePendingConfirmation(pendingID, call.Name, input, execCtx)
				return DispatchResult{
					ToolCalls: records,
					Pending:   &PendingConfirmation{PendingID: pendingID, ToolName: call.Name, Input: input},
				}, nil
			}

			rec := d.executeOne(ctx, execCtx, tool, call)
			records = append(records, rec)
			conversation = append(conversation, toolResultMessage(call.ID, rec))
		}
	}

	rcs := collectRichContents(records)
	return DispatchResult{FinalAnswer: lastAnswer, ToolCalls: records, RichContent: firstRichContent(records), RichContents: rcs}, nil
}

// buildFallbackAnswer renders whatever tool results were gathered before a
// follow-up LLM round failed, so the user still gets something useful
// instead of a hard error (Edge Case, FR-005).
func buildFallbackAnswer(records []ToolCallRecord) string {
	var b strings.Builder
	b.WriteString("AI gặp lỗi tạm thời khi tổng hợp câu trả lời, nhưng đây là kết quả từ các tool đã chạy:\n\n")
	for _, rec := range records {
		switch rec.Status {
		case "success":
			fmt.Fprintf(&b, "- %s: %s\n", rec.ToolName, string(rec.Output))
		case "timeout":
			fmt.Fprintf(&b, "- %s: hết thời gian chờ (%s)\n", rec.ToolName, rec.ErrorMessage)
		default:
			fmt.Fprintf(&b, "- %s: lỗi (%s)\n", rec.ToolName, rec.ErrorMessage)
		}
	}
	return b.String()
}

func (d *Dispatcher) toolSchemas() []utils.ToolSchema {
	all := d.Registry.All()
	schemas := make([]utils.ToolSchema, 0, len(all))
	for _, t := range all {
		schemas = append(schemas, utils.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}
	return schemas
}

type executeOutcome struct {
	result ToolResult
	err    error
}

// executeOne runs a single tool call with its declared timeout, tier-limit
// check, and panic recovery so a misbehaving tool can never crash the chat
// request (FR-005). Always logs exactly one ToolCallRecord (FR-006).
func (d *Dispatcher) executeOne(ctx context.Context, execCtx ToolExecutionContext, tool Tool, call utils.ToolCallRequest) ToolCallRecord {
	start := time.Now()
	input := argumentsToRawMessage(call.Arguments)

	rec := ToolCallRecord{
		ToolName:  tool.Name(),
		UserID:    execCtx.UserID,
		SessionID: execCtx.SessionID,
		Input:     input,
	}

	if limits := tool.TierLimit(); limits != nil {
		if allowed, retryAfter := checkTierLimit(ctx, tool.Name(), execCtx.UserID, execCtx.Tier, limits); !allowed {
			rec.Status = "error"
			rec.ErrorMessage = fmt.Sprintf("vượt giới hạn gọi tool theo tier, thử lại sau %s", retryAfter)
			rec.DurationMs = int(time.Since(start).Milliseconds())
			LogToolCall(rec)
			return rec
		}
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, tool.Timeout())
	defer cancel()

	resultCh := make(chan executeOutcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				resultCh <- executeOutcome{err: fmt.Errorf("tool panic: %v", r)}
			}
		}()
		res, err := tool.Execute(timeoutCtx, execCtx, input)
		resultCh <- executeOutcome{result: res, err: err}
	}()

	select {
	case outcome := <-resultCh:
		rec.DurationMs = int(time.Since(start).Milliseconds())
		if outcome.err != nil {
			rec.Status = "error"
			rec.ErrorMessage = outcome.err.Error()
		} else {
			rec.Status = "success"
			rec.Output = resultToJSON(outcome.result)
			rec.RichContent = outcome.result.RichContent
		}
	case <-timeoutCtx.Done():
		rec.DurationMs = int(time.Since(start).Milliseconds())
		rec.Status = "timeout"
		rec.ErrorMessage = fmt.Sprintf("tool %q vượt timeout %s", tool.Name(), tool.Timeout())
	}

	LogToolCall(rec)
	return rec
}

func firstRichContent(records []ToolCallRecord) json.RawMessage {
	for _, r := range records {
		if len(r.RichContent) > 0 {
			return r.RichContent
		}
	}
	return nil
}

func collectRichContents(records []ToolCallRecord) []json.RawMessage {
	var out []json.RawMessage
	for _, r := range records {
		if len(r.RichContent) > 0 {
			out = append(out, r.RichContent)
		}
	}
	return out
}

func argumentsToRawMessage(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(trimmed)
}

func resultToJSON(r ToolResult) json.RawMessage {
	if len(r.JSONOutput) > 0 {
		return r.JSONOutput
	}
	encoded, _ := json.Marshal(map[string]string{"text": r.TextOutput, "file_url": r.FileURL})
	return encoded
}

const maxToolResultForLLM = 2000

func toolResultMessage(toolCallID string, rec ToolCallRecord) utils.ChatMessage {
	var content string
	if rec.Status == "success" {
		content = string(rec.Output)
		if len(content) > maxToolResultForLLM {
			content = content[:maxToolResultForLLM] + "\n...(dữ liệu đã được rút gọn, tổng hợp từ phần trên)"
		}
	} else {
		encoded, _ := json.Marshal(map[string]string{"error": rec.ErrorMessage})
		content = string(encoded)
	}
	return utils.ChatMessage{Role: "tool", ToolCallID: toolCallID, Content: content}
}

// checkTierLimit reuses the Redis-counter pattern already established by
// utils.CheckWebSearchUserLimit, generalized to any tool's per-tier hourly
// limit (FR-012).
func checkTierLimit(ctx context.Context, toolName, userID, tier string, limits map[string]int) (bool, time.Duration) {
	if config.RedisClient == nil || strings.TrimSpace(userID) == "" {
		return true, 0
	}
	limit, ok := limits[strings.ToUpper(tier)]
	if !ok || limit <= 0 {
		return true, 0
	}
	window := time.Hour
	key := fmt.Sprintf("tools:limit:%s:%s:%s", toolName, strings.ToUpper(tier), userID)
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

// pendingPayload is what's persisted in Redis between the dispatcher
// returning DispatchResult.Pending and a later confirm/cancel call.
type pendingPayload struct {
	ToolName  string          `json:"tool_name"`
	Input     json.RawMessage `json:"input"`
	UserID    string          `json:"user_id"`
	SessionID string          `json:"session_id"`
	Tier      string          `json:"tier"`
}

func pendingConfirmationKey(pendingID string) string {
	return fmt.Sprintf("tools:pending:%s", pendingID)
}

// storePendingConfirmation persists the not-yet-executed call so a follow-up
// confirm request can resume it. Best-effort: if Redis is unavailable, the
// dispatcher still returns Pending and the caller is responsible for holding
// state itself (e.g. by re-sending the original tool call on confirm).
func storePendingConfirmation(pendingID, toolName string, input json.RawMessage, execCtx ToolExecutionContext) {
	if config.RedisClient == nil {
		return
	}
	payload, err := json.Marshal(pendingPayload{
		ToolName:  toolName,
		Input:     input,
		UserID:    execCtx.UserID,
		SessionID: execCtx.SessionID,
		Tier:      execCtx.Tier,
	})
	if err != nil {
		log.Printf("⚠️ [Tools] failed to marshal pending confirmation %s: %v", pendingID, err)
		return
	}
	if err := config.RedisClient.Set(config.Ctx, pendingConfirmationKey(pendingID), payload, 5*time.Minute).Err(); err != nil {
		log.Printf("⚠️ [Tools] failed to store pending confirmation %s: %v", pendingID, err)
	}
}

// popPendingConfirmation reads and deletes (consume-once) a pending
// confirmation record, so the same confirmation can never be replayed twice.
func popPendingConfirmation(ctx context.Context, pendingID string) (pendingPayload, error) {
	if config.RedisClient == nil {
		return pendingPayload{}, fmt.Errorf("xác nhận không khả dụng (Redis chưa kết nối)")
	}
	key := pendingConfirmationKey(pendingID)
	raw, err := config.RedisClient.Get(ctx, key).Result()
	if err != nil {
		return pendingPayload{}, fmt.Errorf("xác nhận đã hết hạn hoặc không tồn tại")
	}
	config.RedisClient.Del(ctx, key)

	var payload pendingPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return pendingPayload{}, fmt.Errorf("dữ liệu xác nhận bị lỗi")
	}
	return payload, nil
}

// CancelPendingConfirmation discards a pending side-effect tool call without
// executing it (user declined the confirmation prompt, US9 AC1 "no" path).
func CancelPendingConfirmation(ctx context.Context, pendingID string) {
	if config.RedisClient == nil {
		return
	}
	config.RedisClient.Del(ctx, pendingConfirmationKey(pendingID))
}

// ResolvePendingConfirmation executes a previously-held side-effect tool
// call after the user has explicitly confirmed it (FR-009, SC-008).
// requestingUserID must match the user who originally triggered the
// confirmation - otherwise the record is consumed but not executed, so a
// stolen/guessed pendingID still cannot be replayed.
func (d *Dispatcher) ResolvePendingConfirmation(ctx context.Context, pendingID, requestingUserID string) (ToolCallRecord, error) {
	payload, err := popPendingConfirmation(ctx, pendingID)
	if err != nil {
		return ToolCallRecord{}, err
	}
	if payload.UserID != requestingUserID {
		return ToolCallRecord{}, fmt.Errorf("bạn không có quyền xác nhận hành động này")
	}

	tool, ok := d.Registry.Get(payload.ToolName)
	if !ok {
		return ToolCallRecord{}, fmt.Errorf("tool %q không còn tồn tại trong registry", payload.ToolName)
	}

	execCtx := ToolExecutionContext{UserID: payload.UserID, SessionID: payload.SessionID, Tier: payload.Tier}
	call := utils.ToolCallRequest{ID: pendingID, Name: payload.ToolName, Arguments: string(payload.Input)}
	return d.executeOne(ctx, execCtx, tool, call), nil
}
