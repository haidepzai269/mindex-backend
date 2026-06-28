package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/internal/persona"
	"mindex-backend/internal/tools"
	"mindex-backend/utils"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const globalAIChatScope = "global_ai"

type GlobalAIChatRequest struct {
	SessionID         string `json:"session_id"`
	Question          string `json:"question" binding:"required"`
	Model             string `json:"model"`
	Thinking          bool   `json:"thinking"`
	VideoAttachmentID string `json:"video_attachment_id"`
}

func CreateGlobalAISession(c *gin.Context) {
	userID := c.GetString("user_id")
	sessionID := uuid.New().String()
	title := "Mindex AI Tổng hợp"

	_, err := config.DB.Exec(config.Ctx, `
		INSERT INTO chat_histories (user_id, session_id, full_messages, started_at, title, chat_scope)
		VALUES ($1, $2, '[]'::jsonb, NOW(), $3, $4)`,
		userID, sessionID, title, globalAIChatScope)
	if err != nil {
		log.Printf("[GLOBAL_AI] Failed to create session: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "INTERNAL_ERROR", "message": "Không thể tạo phiên chat"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"data": gin.H{
			"session_id": sessionID,
			"title":      title,
		},
	})
}

func GetActiveGlobalAISession(c *gin.Context) {
	userID := c.GetString("user_id")

	var sessionID string
	err := config.DB.QueryRow(config.Ctx, `
		SELECT session_id FROM chat_histories
		WHERE user_id = $1 AND chat_scope = $2 AND message_count > 0
		ORDER BY started_at DESC LIMIT 1`, userID, globalAIChatScope).Scan(&sessionID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": nil})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"session_id": sessionID,
		},
	})
}

func GetGlobalAISessionMessages(c *gin.Context) {
	sessionID := c.Param("session_id")
	userID := c.GetString("user_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "MISSING_SESSION_ID"})
		return
	}

	limit := parsePageInt(c.Query("limit"), 0, 0, 80)
	skip := parsePageInt(c.Query("skip"), 0, 0, 10000)

	var fullMessages []byte
	err := config.DB.QueryRow(config.Ctx, `
		SELECT full_messages FROM chat_histories
		WHERE session_id = $1 AND user_id = $2 AND chat_scope = $3`,
		sessionID, userID, globalAIChatScope).Scan(&fullMessages)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data": gin.H{
				"session_id": sessionID,
				"messages":   []interface{}{},
				"total":      0,
				"has_more":   false,
			},
		})
		return
	}

	allMessages := hydrateGlobalAILogIDs(sessionID, userID, fullMessages)
	total := len(allMessages)
	var paged []map[string]interface{}
	hasMore := false

	if limit > 0 {
		end := total - skip
		if end < 0 {
			end = 0
		}
		start := end - limit
		if start < 0 {
			start = 0
		}
		paged = allMessages[start:end]
		hasMore = start > 0
	} else {
		paged = allMessages
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"session_id": sessionID,
			"messages":   paged,
			"total":      total,
			"has_more":   hasMore,
		},
	})
}

func RenameGlobalAISession(c *gin.Context) {
	sessionID := c.Param("session_id")
	userID := c.GetString("user_id")

	var req struct {
		Title string `json:"title" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Title) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Thiếu title"})
		return
	}

	res, err := config.DB.Exec(config.Ctx, `
		UPDATE chat_histories
		SET title = $1
		WHERE session_id = $2 AND user_id = $3 AND chat_scope = $4`,
		strings.TrimSpace(req.Title), sessionID, userID, globalAIChatScope)
	if err != nil || res.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Không tìm thấy session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Đã đổi tên session"})
}

func DeleteGlobalAISession(c *gin.Context) {
	sessionID := c.Param("session_id")
	userID := c.GetString("user_id")

	res, err := config.DB.Exec(config.Ctx, `
		DELETE FROM chat_histories
		WHERE session_id = $1 AND user_id = $2 AND chat_scope = $3`,
		sessionID, userID, globalAIChatScope)
	if err != nil || res.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Không tìm thấy session"})
		return
	}

	if config.RedisClient != nil {
		config.RedisClient.Del(context.Background(), "session:"+sessionID)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Đã xóa session"})
}

func GlobalAIMessage(c *gin.Context) {
	var req GlobalAIChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Tham số không hợp lệ"})
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "MISSING_PARAMS", "message": "Thiếu câu hỏi"})
		return
	}

	userID := c.GetString("user_id")
	userPersona := c.GetString("persona")
	if userPersona == "" {
		userPersona = "student"
	}
	userTier := getUserTierForChat(userID, c.GetString("role"))
	thinkingMode := req.Thinking && isTierAllowedThinking(userTier)

	if !isValidGlobalAISession(req.SessionID, userID) {
		req.SessionID = ""
	}

	systemPrompt := persona.Cache.GetChatPrompt(userPersona)
	personaCfg := persona.Cache.Get(userPersona)
	systemPrompt += buildGlobalAISystemPrompt()

	if req.SessionID == "" {
		req.SessionID = uuid.New().String()
		_, err := config.DB.Exec(config.Ctx, `
			INSERT INTO chat_histories (user_id, session_id, full_messages, started_at, title, chat_scope)
			VALUES ($1, $2, '[]'::jsonb, NOW(), $3, $4)`,
			userID, req.SessionID, "Mindex AI Tổng hợp", globalAIChatScope)
		if err != nil {
			log.Printf("[GLOBAL_AI] Failed to create session: %v", err)
		}

		if personaCfg.RequireDisclaimer && personaCfg.DisclaimerText != nil {
			systemPrompt += "\n\nSESSION START DISCLAIMER: " + *personaCfg.DisclaimerText
		}
	}

	userMsg := gin.H{
		"id":        uuid.New().String(),
		"role":      "user",
		"content":   req.Question,
		"timestamp": time.Now().Format(time.RFC3339),
	}
	if req.VideoAttachmentID != "" {
		var storageURL, originalName, videoMimeType string
		var durationSec int
		var sizeBytes int64
		if err := config.DB.QueryRow(config.Ctx, `
			SELECT COALESCE(storage_url, ''), original_name, mime_type, duration_seconds, size_bytes
			FROM chat_video_attachments
			WHERE id = $1 AND user_id = $2`,
			req.VideoAttachmentID, userID).Scan(&storageURL, &originalName, &videoMimeType, &durationSec, &sizeBytes); err == nil {
			userMsg["attachments"] = []gin.H{{
				"id":               req.VideoAttachmentID,
				"url":              storageURL,
				"filename":         originalName,
				"mime_type":        videoMimeType,
				"size_bytes":       sizeBytes,
				"type":             "video",
				"duration_seconds": durationSec,
			}}
		}
	}
	userMsgBytes, _ := json.Marshal(userMsg)
	if err := appendChatHistoryMessage(config.Ctx, req.SessionID, string(userMsgBytes)); err != nil {
		log.Printf("[GLOBAL_AI] Failed to save user message: %v", err)
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Streaming unsupported"})
		return
	}
	writeChatStatus(c, flusher, "thinking")
	writeChatInsight(c, flusher, fmt.Sprintf("Đã nhận câu hỏi Global AI: \"%s\".", compactChatPreview(req.Question, 120)))

	// [Phase C] Keyword pre-filter cho social messages rõ ràng
	if utils.IsSensitiveContent(req.Question) {
		log.Printf("⚡ [GLOBAL_AI] Keyword pre-filter: sensitive content detected for session=%s q=%q", req.SessionID, req.Question)
		sensitiveMsg := "Xin lỗi, tôi không tìm thấy nội dung liên quan đến câu hỏi này trong tài liệu. Hãy thử hỏi về các nội dung được đề cập trong tài liệu."
		sendHardcodedSSEResponse(c, flusher, req.SessionID, sensitiveMsg)
		go saveHardcodedToHistory(req.SessionID, req.Question, sensitiveMsg)
		return
	}

	if isObviouslyOffTopic(req.Question) {
		log.Printf("⚡ [GLOBAL_AI] Keyword pre-filter: obvious off-topic for session=%s q=%q", req.SessionID, req.Question)
		socialMsg := "Tôi là Mindex AI, chuyên hỗ trợ học tập dựa trên tài liệu trong Thư viện chung Mindex. Hãy hỏi tôi về các chủ đề học thuật, khái niệm hoặc nội dung tài liệu bạn muốn tìm hiểu!"
		sendHardcodedSSEResponse(c, flusher, req.SessionID, socialMsg)
		go saveHardcodedToHistory(req.SessionID, req.Question, socialMsg)
		return
	}

	// [Cache Check] Kiểm tra answer cache trước mọi API call (chỉ cho non-thinking mode)
	if !thinkingMode {
		cacheKey := utils.AnswerCacheKey(req.Question, "global", userPersona)
		if cached, ok := utils.GetAnswerCache(context.Background(), cacheKey); ok {
			log.Printf("🎯 [GLOBAL_AI] Cache HIT session=%s", req.SessionID)
			writeChatInsight(c, flusher, "Đã tìm thấy câu trả lời trong bộ nhớ đệm, trả về kết quả ngay lập tức.")
			sendCachedSSEResponse(c, flusher, req.SessionID, cached)
			go saveHardcodedToHistory(req.SessionID, req.Question, cached.Answer)
			return
		}
	}

	writeChatInsight(c, flusher, "Đang kiểm tra lịch sử phiên và Thư viện chung Mindex để dựng ngữ cảnh.")

	historySummary := loadSessionHistorySummary(req.SessionID)
	isFirstMessage := historySummary == ""
	systemPrompt = utils.ApplyMindexBranding(systemPrompt, isFirstMessage)
	if isFirstMessage {
		writeChatInsight(c, flusher, "Đây là lượt hỏi đầu trong phiên Global AI, AI sẽ ưu tiên trả lời rõ phạm vi nguồn.")
	} else {
		writeChatInsight(c, flusher, "Đã tìm thấy lịch sử phiên, AI sẽ dùng nó để hiểu câu hỏi nối tiếp.")
	}

	searchQuery := utils.RewriteQueryWithHistory(req.Question, historySummary)
	if strings.TrimSpace(searchQuery) != "" && searchQuery != req.Question {
		writeChatInsight(c, flusher, fmt.Sprintf("Đã diễn giải lại câu hỏi để tìm kiếm tốt hơn: \"%s\".", compactChatPreview(searchQuery, 140)))
	}
	writeChatInsight(c, flusher, "Đang tạo embedding để tìm nguồn liên quan trong Thư viện chung Mindex.")
	queryVec, err := utils.GeminiEmbedPool.EmbedWithRetry(searchQuery, utils.CallGeminiAPI)
	if err != nil {
		fmt.Fprintf(c.Writer, "event: error\ndata: {\"error\": \"EMBEDDING_FAILED\"}\n\n")
		flusher.Flush()
		return
	}

	log.Printf("[GLOBAL_AI] Hybrid search session=%s query=%s", req.SessionID, searchQuery)
	searchResults, _ := utils.GlobalHybridSearch(searchQuery, queryVec, 12)
	writeChatInsight(c, flusher, fmt.Sprintf("Hybrid search tìm thấy %d đoạn ứng viên trong Thư viện chung.", len(searchResults)))

	var contextText string
	var mindexContext string
	var sources []map[string]interface{}
	for _, res := range searchResults {
		chunkContext := fmt.Sprintf("Tài liệu: %s\nĐoạn %d (Trang %d): %s\n\n",
			res.DocTitle, res.ChunkIndex, res.PageNumber, res.RetrievalContent)
		contextText += chunkContext
		mindexContext += chunkContext
		sources = append(sources, map[string]interface{}{
			"type":        "document",
			"document_id": res.DocID,
			"doc_title":   res.DocTitle,
			"chunk_index": res.ChunkIndex,
			"page":        res.PageNumber,
			"page_number": res.PageNumber,
			"score":       res.Score,
			"similarity":  res.Score,
			"content":     res.RetrievalContent,
		})
	}

	hasMindexContext := strings.TrimSpace(mindexContext) != ""
	if hasMindexContext {
		writeChatInsight(c, flusher, fmt.Sprintf("Đã gom %d nguồn Mindex vào ngữ cảnh trả lời.", len(sources)))
	} else {
		writeChatInsight(c, flusher, "Không tìm thấy nguồn Mindex đủ gần, AI sẽ cân nhắc web search hoặc trả lời với cảnh báo phạm vi nguồn.")
	}
	var webSearchUsed bool
	var webSearchMeta gin.H
	isToolQuery := isToolDomainQuery(req.Question)
	if config.Env.WebSearchEnabled && !isToolQuery {
		webPlan := utils.DecideWebSearch(req.Question, searchQuery, mindexContext, "", userPersona, userTier, thinkingMode)
		if !hasMindexContext {
			webPlan.UseWebSearch = true
			if strings.TrimSpace(webPlan.Query) == "" {
				webPlan.Query = searchQuery
			}
			webPlan.Reason = "Không tìm thấy nguồn phù hợp trong Thư viện chung Mindex"
		}
		writeChatInsight(c, flusher, fmt.Sprintf("Quyết định web search: %s.", compactChatPreview(webPlan.Reason, 140)))

		webSearchMeta = gin.H{
			"requested": req.Thinking,
			"thinking":  thinkingMode,
			"decision":  webPlan.UseWebSearch,
			"query":     webPlan.Query,
			"reason":    webPlan.Reason,
		}

		if webPlan.UseWebSearch {
			allowed, retryAfter := utils.CheckWebSearchUserLimit(context.Background(), userID, userTier)
			if !allowed {
				webSearchMeta["used"] = false
				webSearchMeta["rate_limited"] = true
				webSearchMeta["retry_after_seconds"] = int(retryAfter.Seconds())
				webSearchMeta["error"] = "web search user rate limit reached"
				writeChatInsight(c, flusher, "Web search bị giới hạn lượt dùng, AI tiếp tục với nguồn Mindex/ngữ cảnh hiện có.")
			} else {
				writeChatStatus(c, flusher, "searching")
				writeChatInsight(c, flusher, fmt.Sprintf("Đang tìm web với truy vấn: \"%s\".", compactChatPreview(webPlan.Query, 140)))
				webCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				webResp, err := utils.SearchWeb(webCtx, webPlan)
				cancel()
				writeChatStatus(c, flusher, "thinking")
				if err != nil {
					log.Printf("[GLOBAL_AI] Web search skipped after decision: %v", err)
					webSearchMeta["error"] = err.Error()
					writeChatInsight(c, flusher, "Web search không khả dụng, AI tiếp tục với ngữ cảnh hiện có.")
				} else {
					webContext := utils.FormatWebSearchContext(webResp)
					if webContext != "" {
						contextText += "\n\n" + webContext
						sources = append(sources, utils.WebSearchSources(webResp)...)
						systemPrompt += buildWebSearchPromptRules(userPersona, thinkingMode)
						webSearchUsed = true
						webSearchMeta["used"] = true
						webSearchMeta["provider"] = webResp.Provider
						webSearchMeta["from_cache"] = webResp.FromCache
						webSearchMeta["results"] = len(webResp.Results)
						writeChatInsight(c, flusher, fmt.Sprintf("Đã bổ sung %d kết quả web từ %s.", len(webResp.Results), webResp.Provider))
					}
				}
			}
		}
	} else if isToolQuery {
		webSearchMeta = gin.H{
			"used":   false,
			"reason": "skipped: tool domain query (crypto/news/weather)",
		}
		writeChatInsight(c, flusher, "Câu hỏi thuộc domain realtime tool, bỏ qua web search để ưu tiên tool dispatcher.")
	} else if req.Thinking || !hasMindexContext {
		webSearchMeta = gin.H{
			"requested": req.Thinking,
			"thinking":  thinkingMode,
			"used":      false,
			"reason":    "web search disabled",
		}
		writeChatInsight(c, flusher, "Web search đang tắt, AI sẽ trả lời theo nguồn Mindex và kiến thức model với cảnh báo phù hợp.")
	}

	if thinkingMode {
		systemPrompt += `

[THINKING MODE]
User đã bật chế độ Thinking cho câu hỏi này. Hãy phân tích kỹ hơn, kiểm tra mâu thuẫn giữa các nguồn, và trả lời có cấu trúc rõ ràng. Không bịa nguồn.`
	}

	var videoContext string
	if req.VideoAttachmentID != "" {
		videoCtxText, err := loadChatVideoAttachmentForPrompt(userID, req.SessionID, req.VideoAttachmentID)
		if err != nil {
			log.Printf("[GLOBAL_AI] Video context load failed id=%s: %v", req.VideoAttachmentID, err)
		} else {
			videoContext = buildChatVideoContext(videoCtxText)
			if videoContext != "" {
				writeChatInsight(c, flusher, "Đã tải ngữ cảnh phân tích video vào prompt.")
			}
		}
	}
	hasVideoContext := videoContext != ""

	// Short-circuit: không có context nào → bỏ qua AI call
	// Ngoại trừ tool domain queries (crypto, news, weather) — tool dispatcher sẽ xử lý
	if !hasMindexContext && !webSearchUsed && !hasVideoContext && !isToolQuery {
		log.Printf("⚡ [GLOBAL_AI] No context (mindex=false, web=false, video=false) for session=%s, short-circuiting AI call", req.SessionID)
		noContextMsg := "Xin lỗi, tôi không tìm thấy thông tin phù hợp trong Thư viện chung Mindex. Hãy thử hỏi về chủ đề khác hoặc tìm kiếm bằng từ khóa cụ thể hơn."
		sendHardcodedSSEResponse(c, flusher, req.SessionID, noContextMsg)
		go persistGlobalAIConversation(req.SessionID, req.Question, noContextMsg, nil, "", userTier, nil /*richContents*/)
		return
	}

	if !hasMindexContext && !hasVideoContext {
		systemPrompt += `

[MINDEX SOURCE STATUS]
Không tìm thấy nguồn phù hợp trong Thư viện chung Mindex cho câu hỏi này. Nếu dùng kiến thức model hoặc nguồn web, hãy nói rõ: "không tìm thấy nguồn phù hợp trong Thư viện chung Mindex". Không gán nguồn Mindex cho thông tin không có trong context.`
	}

	if hasVideoContext {
		contextText = videoContext + "\n" + contextText
	}

	if strings.TrimSpace(contextText) == "" {
		contextText = "Không có nguồn Thư viện chung Mindex phù hợp và không có web context khả dụng."
	}

	finalPrompt := buildRAGPrompt(systemPrompt, historySummary, contextText, req.Question)
	messages := []utils.ChatMessage{
		{Role: "system", Content: finalPrompt},
		{Role: "user", Content: req.Question},
	}

	writeChatInsight(c, flusher, "Đang gửi prompt đã dựng sang AI Orchestrator để bắt đầu sinh câu trả lời.")
	sendToolStatusHints(c, flusher, req.Question)
	chatStart := time.Now()

	// AI Tool Framework (spec 008): let the model call registered tools
	// (calculator, web_search, rag_search, ...) before producing its final
	// answer. The dispatcher's loop is non-streaming by nature (it needs the
	// full response to detect tool_calls), so the final answer is sent as
	// word-chunked SSE tokens via writeChatTokens instead of true
	// token-by-token streaming - a known trade-off for this integration.
	var fullAnswer string
	var usedProvider utils.ProviderType
	var streamErr error

	execCtx := tools.ToolExecutionContext{UserID: userID, SessionID: req.SessionID, Tier: userTier}
	dispatchResult, dispatchErr := tools.NewDispatcher(tools.Default).Run(c.Request.Context(), execCtx, messages, 0)

	switch {
	case dispatchErr != nil:
		streamErr = dispatchErr
	case dispatchResult.Pending != nil:
		fullAnswer = fmt.Sprintf("Mình cần xác nhận trước khi thực hiện \"%s\". Bạn xác nhận để mình tiếp tục không?", dispatchResult.Pending.ToolName)
		writeChatTokens(c, flusher, fullAnswer)
	default:
		for _, rc := range dispatchResult.RichContents {
			writeChatRichContent(c, flusher, rc)
		}
		fullAnswer = dispatchResult.FinalAnswer
		usedProvider = "tool_dispatcher"
		writeChatTokens(c, flusher, fullAnswer)
	}

	chatLatency := int(time.Since(chatStart).Milliseconds())

	// [Cache Write] Lưu câu trả lời vào cache sau khi AI thành công (async)
	if !thinkingMode && streamErr == nil && fullAnswer != "" {
		cacheKey := utils.AnswerCacheKey(req.Question, "global", userPersona)
		go utils.SetAnswerCache(context.Background(), cacheKey, fullAnswer, sources, utils.AnswerCacheTTLGlobal)
	}

	var logID string
	if streamErr == nil && fullAnswer != "" {
		logID = uuid.New().String()
		entry := AIResponseLogEntry{
			ID:           logID,
			SessionID:    req.SessionID,
			UserID:       userID,
			Question:     req.Question,
			Answer:       fullAnswer,
			ModelUsed:    string(usedProvider),
			LatencyMs:    chatLatency,
			TokenCount:   (len(finalPrompt) + len(fullAnswer)) / 4,
			SourcesCount: len(sources),
			RichContent:  dispatchResult.RichContent,
		}
		go SaveAIResponseLogWithID(entry)
	}

	go func() {
		status := "ok"
		if streamErr != nil {
			status = "error"
		}
		utils.LogTokenUsage(utils.TokenUsageLog{
			UserID:      &userID,
			DocumentID:  nil,
			SessionID:   req.SessionID,
			Service:     string(usedProvider),
			Operation:   "chat_global_ai",
			TotalTokens: (len(finalPrompt) + len(fullAnswer)) / 4,
			LatencyMs:   chatLatency,
			KeyAlias:    "auto_fallback",
			Status:      status,
		})
	}()

	if streamErr != nil {
		log.Printf("[GLOBAL_AI] AI providers failed: %v", streamErr)
		fmt.Fprintf(c.Writer, "event: error\ndata: {\"error\": \"AI_SERVICE_DOWN\"}\n\n")
		flusher.Flush()
		return
	}
	writeChatInsight(c, flusher, fmt.Sprintf("Hoàn tất sinh câu trả lời bằng provider %s trong %.1fs.", usedProvider, float64(chatLatency)/1000))

	donePayload := gin.H{
		"session_id":  req.SessionID,
		"message_id":  uuid.New().String(),
		"log_id":      logID,
		"answer":      fullAnswer,
		"sources":     sources,
		"web_search":  webSearchMeta,
		"chat_scope":  globalAIChatScope,
		"mindex_used": hasMindexContext,
	}
	if dispatchResult.Pending != nil {
		donePayload["pending_confirmation"] = gin.H{
			"pending_id": dispatchResult.Pending.PendingID,
			"tool_name":  dispatchResult.Pending.ToolName,
		}
	}
	doneBytes, _ := json.Marshal(donePayload)
	fmt.Fprintf(c.Writer, "event: done\ndata: %s\n\n", string(doneBytes))
	flusher.Flush()

	persistGlobalAIConversation(req.SessionID, req.Question, fullAnswer, sources, logID, userTier, dispatchResult.RichContents)
}

func isValidGlobalAISession(sessionID string, userID string) bool {
	if strings.TrimSpace(sessionID) == "" {
		return false
	}

	var exists bool
	err := config.DB.QueryRow(config.Ctx, `
		SELECT EXISTS(
			SELECT 1 FROM chat_histories
			WHERE session_id = $1 AND user_id = $2 AND chat_scope = $3
		)`, sessionID, userID, globalAIChatScope).Scan(&exists)
	if err != nil {
		log.Printf("[GLOBAL_AI] Session check failed: %v", err)
		return false
	}
	return exists
}

func loadSessionHistorySummary(sessionID string) string {
	if config.RedisClient == nil {
		return loadDBSessionSummary(sessionID)
	}

	historyKey := "session:" + sessionID
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rawHistory, err := config.RedisClient.LRange(ctx, historyKey, -3, -1).Result()
	if err != nil {
		log.Printf("[GLOBAL_AI] Redis history skipped: %v", err)
		return loadDBSessionSummary(sessionID)
	}

	var historySummary string
	for _, item := range rawHistory {
		var qa QAHistory
		if err := json.Unmarshal([]byte(item), &qa); err == nil {
			historySummary += fmt.Sprintf("Human: %s\nAssistant: %s\n", qa.Question, qa.Answer)
		}
	}
	if historySummary == "" {
		return loadDBSessionSummary(sessionID)
	}
	return historySummary
}

func loadDBSessionSummary(sessionID string) string {
	var summary *string
	err := config.DB.QueryRow(config.Ctx, `
		SELECT summary FROM chat_histories WHERE session_id = $1 AND chat_scope = $2`,
		sessionID, globalAIChatScope).Scan(&summary)
	if err != nil || summary == nil || *summary == "" {
		return ""
	}
	return "Tóm tắt phiên trước: " + *summary
}

func tierContextTurns(tier string) int64 {
	switch strings.ToUpper(strings.TrimSpace(tier)) {
	case "ULTRA", "ADMIN":
		return 30
	case "PRO":
		return 15
	default:
		return 5
	}
}

var toolHintKeywords = map[string][]string{
	"Đang tra thời tiết...":     {"thời tiết", "nhiệt độ", "weather", "dự báo", "mưa", "nắng", "trời"},
	"Đang tìm tin tức...":       {"tin tức", "tin hot", "news", "báo", "trending"},
	"Đang kiểm tra giá coin...": {"giá coin", "crypto", "bitcoin", "btc", "eth", "giá tiền", "tiền điện tử"},
}

func sendToolStatusHints(c *gin.Context, flusher http.Flusher, question string) {
	q := strings.ToLower(question)
	for hint, keywords := range toolHintKeywords {
		for _, kw := range keywords {
			if strings.Contains(q, kw) {
				writeChatInsight(c, flusher, hint)
				break
			}
		}
	}
}

func isToolDomainQuery(question string) bool {
	q := strings.ToLower(question)
	for _, keywords := range toolHintKeywords {
		for _, kw := range keywords {
			if strings.Contains(q, kw) {
				return true
			}
		}
	}
	return false
}

func persistGlobalAIConversation(sessionID string, question string, answer string, sources []map[string]interface{}, logID string, userTier string, richContents []json.RawMessage) {
	go func() {
		contextTurns := tierContextTurns(userTier)
		if config.RedisClient != nil {
			historyKey := "session:" + sessionID
			newQA := QAHistory{Question: question, Answer: answer}
			qaBytes, _ := json.Marshal(newQA)
			config.RedisClient.RPush(context.Background(), historyKey, string(qaBytes))
			config.RedisClient.LTrim(context.Background(), historyKey, -contextTurns, -1)
			config.RedisClient.Expire(context.Background(), historyKey, 2*time.Hour)
		}

		assistantMsg := gin.H{
			"id":        uuid.New().String(),
			"role":      "assistant",
			"content":   answer,
			"sources":   sources,
			"timestamp": time.Now().Format(time.RFC3339),
			"log_id":    logID,
		}
		if len(richContents) > 0 {
			assistantMsg["rich_contents"] = richContents
		}
		asstMsgBytes, _ := json.Marshal(assistantMsg)
		if err := appendChatHistoryMessage(context.Background(), sessionID, string(asstMsgBytes)); err != nil {
			log.Printf("[GLOBAL_AI] Failed to save assistant message: %v", err)
		}

		shortSummary := question
		if len([]rune(shortSummary)) > 180 {
			shortSummary = string([]rune(shortSummary)[:180]) + "…"
		}
		config.DB.Exec(context.Background(), `
			UPDATE chat_histories SET summary = $1 WHERE session_id = $2 AND summary IS NULL`,
			shortSummary, sessionID)

		// Tự động đặt tên session sau lượt hội thoại đầu tiên
		var msgCount int
		config.DB.QueryRow(context.Background(),
			`SELECT message_count FROM chat_histories WHERE session_id = $1`, sessionID).Scan(&msgCount)
		if msgCount <= 2 {
			go autoGenerateSessionTitle(sessionID, question, answer)
		}
	}()
}

func autoGenerateSessionTitle(sessionID, question, answer string) {
	answerPreview := answer
	if len([]rune(answerPreview)) > 300 {
		answerPreview = string([]rune(answerPreview)[:300]) + "..."
	}

	prompt := fmt.Sprintf(
		"Dựa vào cuộc trò chuyện sau, hãy tạo một tiêu đề ngắn gọn (5-8 từ) bằng tiếng Việt, mô tả chủ đề chính. Chỉ trả về tiêu đề, không có dấu ngoặc kép, không giải thích thêm.\n\nCâu hỏi: %s\n\nCâu trả lời: %s",
		question, answerPreview,
	)

	messages := []utils.ChatMessage{
		{Role: "user", Content: prompt},
	}

	title, _, err := utils.AI.ChatNonStream(utils.ServiceTitleGen, messages)
	if err != nil {
		log.Printf("[GLOBAL_AI] autoGenerateSessionTitle failed for session %s: %v", sessionID, err)
		return
	}

	title = strings.TrimSpace(title)
	title = strings.Trim(title, `"'`+"`")
	if len([]rune(title)) > 60 {
		title = string([]rune(title)[:60])
	}
	if title == "" {
		return
	}

	_, err = config.DB.Exec(context.Background(), `
		UPDATE chat_histories SET title = $1
		WHERE session_id = $2 AND title = 'Mindex AI Tổng hợp'`,
		title, sessionID)
	if err != nil {
		log.Printf("[GLOBAL_AI] Failed to update auto title for session %s: %v", sessionID, err)
	}
}

func hydrateGlobalAILogIDs(sessionID string, userID string, fullMessages []byte) []map[string]interface{} {
	var messages []map[string]interface{}
	if len(fullMessages) > 0 {
		_ = json.Unmarshal(fullMessages, &messages)
	}

	changed := false
	for i, msg := range messages {
		role, ok := msg["role"].(string)
		if !ok || role != "assistant" {
			continue
		}
		if _, hasLogID := msg["log_id"]; hasLogID {
			continue
		}

		content, _ := msg["content"].(string)
		var logID string
		err := config.DB.QueryRow(config.Ctx, `
			SELECT id FROM ai_response_logs
			WHERE session_id = $1 AND answer = $2 LIMIT 1`, sessionID, content).Scan(&logID)
		if err != nil {
			logID = uuid.New().String()
			_, insertErr := config.DB.Exec(config.Ctx, `
				INSERT INTO ai_response_logs
				  (id, session_id, user_id, question, answer, model_used, latency_ms, token_count, sources_count)
				VALUES ($1, $2, $3, 'Lịch sử Global AI', $4, 'legacy', 0, 0, 0)`,
				logID, sessionID, userID, content)
			if insertErr != nil {
				log.Printf("[GLOBAL_AI] Failed to insert legacy log: %v", insertErr)
				continue
			}
		}

		messages[i]["log_id"] = logID
		changed = true
	}

	if changed {
		newFullMessages, err := json.Marshal(messages)
		if err == nil {
			_, updateErr := config.DB.Exec(config.Ctx, `
				UPDATE chat_histories
				SET full_messages = $1
				WHERE session_id = $2 AND user_id = $3 AND chat_scope = $4`,
				newFullMessages, sessionID, userID, globalAIChatScope)
			if updateErr != nil {
				log.Printf("[GLOBAL_AI] Failed to update hydrated history: %v", updateErr)
			}
		}
	}

	return messages
}

func ListGlobalAISessions(c *gin.Context) {
	userID := c.GetString("user_id")

	rows, err := config.DB.Query(config.Ctx, `
		SELECT session_id, title, COALESCE(summary, ''), started_at, message_count
		FROM chat_histories
		WHERE user_id = $1 AND chat_scope = $2 AND message_count > 0
		ORDER BY started_at DESC
		LIMIT 50`,
		userID, globalAIChatScope)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false})
		return
	}
	defer rows.Close()

	var sessions []gin.H
	for rows.Next() {
		var sessionID, title, summary string
		var startedAt time.Time
		var messageCount int
		if err := rows.Scan(&sessionID, &title, &summary, &startedAt, &messageCount); err != nil {
			continue
		}
		sessions = append(sessions, gin.H{
			"session_id":    sessionID,
			"title":         title,
			"summary":       summary,
			"started_at":    startedAt.Format(time.RFC3339),
			"message_count": messageCount,
		})
	}
	if sessions == nil {
		sessions = []gin.H{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": sessions})
}

func buildGlobalAISystemPrompt() string {
	base := `

[GLOBAL AI CHAT]
Bạn là Mindex AI Tổng hợp. Bạn trả lời bằng tiếng Việt và có thể tổng hợp kiến thức từ các tài liệu công khai trong Thư viện chung Mindex.
1. Ưu tiên nguồn Thư viện chung Mindex trong [CONTEXT] cho câu hỏi học tập. Khi dùng nguồn Mindex, trích dẫn theo tên tài liệu, trang và đoạn/chunk nếu có.
2. Nếu nhiều tài liệu cùng liên quan, hãy tổng hợp điểm đồng thuận và chỉ rõ mâu thuẫn nếu có.
3. NGOẠI LỆ: Câu hỏi về dữ liệu realtime (tin tức, thời tiết, giá coin/crypto) → BẮT BUỘC dùng tool, KHÔNG trả lời từ context hoặc kiến thức model. Xem mục [AVAILABLE TOOLS].
4. Nếu câu hỏi không có nguồn phù hợp trong Thư viện chung Mindex, bạn được trả lời bằng kiến thức model hoặc web context nếu có, nhưng phải nói rõ "không tìm thấy nguồn phù hợp trong Thư viện chung Mindex".
5. Không bịa tên tài liệu, trang, chunk, URL hoặc nguồn. Chỉ nêu nguồn xuất hiện trong context.
6. Nếu context không đủ để kết luận chắc chắn, nói rõ giới hạn và đưa câu trả lời thận trọng.`

	if toolPrompt := tools.BuildToolUsagePrompt(tools.Default); toolPrompt != "" {
		base += "\n" + toolPrompt
	}
	return base
}
