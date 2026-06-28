package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/internal/persona"
	"mindex-backend/utils"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const maxStoredChatMessages = 80

// offTopicVectorSimThreshold: câu hỏi bị coi là off-topic nếu cosine similarity
// tốt nhất thấp hơn ngưỡng này. Data thực tế: on-topic ~0.51, off-topic ~0.48.
// Hạ xuống 0.46 để tạo buffer đủ xa biên off-topic sau khi đã strip social noise.
const offTopicVectorSimThreshold = 0.46

type ChatRequest struct {
	DocumentID          string                   `json:"document_id"`
	CollectionID        string                   `json:"collection_id"`
	SessionID           string                   `json:"session_id"`
	Question            string                   `json:"question" binding:"required"`
	UseSystemDocs       bool                     `json:"use_system_docs"`
	ForkID              string                   `json:"fork_id"` // ID của shared_link nếu đây là fork session
	Model               string                   `json:"model"`   // Mindex-1 hoặc Mindex-2
	Thinking            bool                     `json:"thinking"`
	AttachmentIDs       []string                 `json:"attachment_ids"`
	AttachmentOverrides []ChatAttachmentOverride `json:"attachment_overrides"`
}

type QAHistory struct {
	Question string `json:"q"`
	Answer   string `json:"a"`
}

func ChatMessage(c *gin.Context) {
	var req ChatRequest
	// Support cả JSON body (POST) và Query params (GET)
	if c.Request.Method == "GET" {
		req.DocumentID = c.Query("doc_id")
		req.Question = c.Query("q")
		req.SessionID = c.Query("session_id")
		req.UseSystemDocs = c.Query("system") == "true"
		req.ForkID = c.Query("fork_id")
		req.Model = c.Query("model")
		req.Thinking = c.Query("thinking") == "true"
	} else {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Tham số không hợp lệ"})
			return
		}
	}

	if req.DocumentID == "" && req.CollectionID == "" {
		c.JSON(400, gin.H{"success": false, "error": "MISSING_PARAMS", "message": "Thiếu document_id hoặc collection_id"})
		return
	}
	if req.Question == "" {
		c.JSON(400, gin.H{"success": false, "error": "MISSING_PARAMS", "message": "Thiếu câu hỏi"})
		return
	}

	userID := c.GetString("user_id")
	userPersona := c.GetString("persona")
	if userPersona == "" {
		userPersona = "student"
	}
	userTier := getUserTierForChat(userID, c.GetString("role"))
	thinkingMode := req.Thinking && isTierAllowedThinking(userTier)

	log.Printf("📥 [CHAT] [User: %s] [Doc: %s] [Col: %s] [Session: %s] Question: %s", userID, req.DocumentID, req.CollectionID, req.SessionID, req.Question)

	// --- MỚI: KIỂM TRA HẾT HẠN (EXPIRED CHECK) ---
	var expiredAt *time.Time
	var targetTitle string
	if req.CollectionID != "" {
		err := config.DB.QueryRow(config.Ctx, "SELECT name FROM collections WHERE id = $1", req.CollectionID).Scan(&targetTitle)
		if err != nil {
			log.Printf("❌ [CHAT] Collection query failed for ID [%s]: %v", req.CollectionID, err)
			c.JSON(404, gin.H{"success": false, "error": "NOT_FOUND", "message": "Bộ tài liệu không tồn tại"})
			return
		}
	} else if req.DocumentID != "" {
		err := config.DB.QueryRow(config.Ctx, "SELECT title, expired_at FROM documents WHERE id = $1", req.DocumentID).Scan(&targetTitle, &expiredAt)
		if err != nil {
			log.Printf("❌ [CHAT] Document query failed for ID [%s]: %v", req.DocumentID, err)
			c.JSON(404, gin.H{"success": false, "error": "NOT_FOUND", "message": "Tài liệu không tồn tại hoặc đã hết hạn"})
			return
		}
	}

	if expiredAt != nil && expiredAt.Before(time.Now()) {
		log.Printf("🚫 [CHAT] Access Denied: %s has expired at %v", targetTitle, expiredAt)
		c.JSON(403, gin.H{
			"success": false,
			"error":   "EXPIRED",
			"message": "Tài liệu này đã hết hạn và đang chờ hệ thống dọn dẹp. Bạn không thể tiếp tục trò chuyện.",
		})
		return
	}
	// ---------------------------------------------

	// Survival of the Fittest: Gia hạn nếu là tài liệu cộng đồng
	if req.DocumentID != "" {
		go RefreshPublicDocExpiry(req.DocumentID)
	}

	// XÁC THỰC SESSION: Kiểm tra xem session_id có thuộc về đúng đối tượng và user_id không
	if req.SessionID != "" {
		var exists bool
		var err error
		if req.CollectionID != "" {
			err = config.DB.QueryRow(config.Ctx, `
				SELECT EXISTS(SELECT 1 FROM chat_histories WHERE session_id = $1 AND collection_id = $2 AND user_id = $3)`,
				req.SessionID, req.CollectionID, userID).Scan(&exists)
		} else {
			err = config.DB.QueryRow(config.Ctx, `
				SELECT EXISTS(SELECT 1 FROM chat_histories WHERE session_id = $1 AND document_id = $2 AND user_id = $3)`,
				req.SessionID, req.DocumentID, userID).Scan(&exists)
		}

		if err != nil {
			log.Printf("⚠️ [CHAT] [Session Check Error] %v", err)
		}

		if !exists {
			log.Printf("⚠️ [CHAT] [Session Mismatch] Session %s doesn't belong to Target. Forcing NEW session.", req.SessionID)
			req.SessionID = "" // Xóa session_id để hệ thống tạo mới bên dưới
		}
	}

	systemPrompt := persona.Cache.GetChatPrompt(userPersona)
	personaCfg := persona.Cache.Get(userPersona)

	// Inject SYS-020 for Collection Chat
	if req.CollectionID != "" {
		systemPrompt += `
[BỔ SUNG SYS-020: Chat với Bộ Tài Liệu]
Bạn đang trả lời dựa trên BỘ TÀI LIỆU gồm nhiều file liên quan.
1. Nếu các tài liệu ĐỒNG THUẬN -> tổng hợp và ghi tất cả nguồn.
2. Nếu các tài liệu KHÁC NHAU -> trình bày song song: 'Theo [File A]: ... | Theo [File B]: ...'
3. Ưu tiên thông tin xuất hiện trong NHIỀU tài liệu hơn.
4. Trích dẫn trang/mục nguồn cụ thể: '(Tên tài liệu, Trang X, Mục Y)'.`
	}

	if req.SessionID == "" {
		req.SessionID = uuid.New().String()
		log.Printf("✨ [CHAT] Creating NEW session: %s", req.SessionID)

		// Tạo bản ghi mới trong chat_histories
		var err error
		if req.CollectionID != "" {
			_, err = config.DB.Exec(config.Ctx, `
				INSERT INTO chat_histories (user_id, collection_id, session_id, full_messages, started_at) 
				VALUES ($1, $2, $3, '[]'::jsonb, NOW())`, userID, req.CollectionID, req.SessionID)
		} else {
			_, err = config.DB.Exec(config.Ctx, `
				INSERT INTO chat_histories (user_id, document_id, session_id, full_messages, started_at) 
				VALUES ($1, $2, $3, '[]'::jsonb, NOW())`, userID, req.DocumentID, req.SessionID)
		}

		if err != nil {
			log.Printf("❌ [DB Error] Failed to create session: %v", err)
		}

		if personaCfg.RequireDisclaimer && personaCfg.DisclaimerText != nil {
			systemPrompt += "\n\nSESSION START DISCLAIMER: " + *personaCfg.DisclaimerText
		}
	}

	// [MỚI] Lấy Document Intelligence (nếu có) để inject vào prompt
	var docIntelStr string
	if req.DocumentID != "" {
		var topic, thesis, docType string
		err := config.DB.QueryRow(config.Ctx, `
			SELECT main_topic, thesis, doc_type 
			FROM document_intelligence WHERE doc_id = $1`, req.DocumentID).Scan(&topic, &thesis, &docType)
		if err == nil {
			docIntelStr = fmt.Sprintf("\n[DOCUMENT MAP]\n- Topic: %s\n- Focus: %s\n- Type: %s\n", topic, thesis, docType)
			log.Printf("🧠 [CHAT] Document Intelligence injected for doc %s", req.DocumentID)
		}
	}
	systemPrompt += docIntelStr

	// FORK: Nếu đây là session fork từ shared_link, inject Shared Context vào system prompt
	if req.ForkID != "" {
		var sharedSummary *string
		var sharedDocID string
		var creatorName string
		forkErr := config.DB.QueryRow(config.Ctx, `
			SELECT sl.summary, sl.document_id, u.display_name
			FROM shared_links sl
			JOIN users u ON u.id = sl.creator_id
			WHERE sl.id = $1`, req.ForkID).Scan(&sharedSummary, &sharedDocID, &creatorName)

		if forkErr == nil && sharedSummary != nil {
			systemPrompt = fmt.Sprintf(`%s

[SHARED CONTEXT]
Người dùng này đang tiếp tục một cuộc hội thoại được chia sẻ bởi "%s".
Dưới đây là tóm tắt hội thoại gốc để cung cấp ngữ cảnh:
%s

Hãy nhận thức được ngữ cảnh này nhưng KHÔNG lặp lại nó. Tập trung trả lời câu hỏi mới của người dùng.
[END SHARED CONTEXT]`, systemPrompt, creatorName, *sharedSummary)
			log.Printf("🔗 [CHAT] Fork context injected from link %s (creator: %s)", req.ForkID, creatorName)
		}
	}

	// 0. Lưu tin nhắn của User vào PostgreSQL
	chatAttachments, err := loadChatImageAttachmentsForPrompt(config.Ctx, userID, req.SessionID, req.AttachmentIDs, req.AttachmentOverrides)
	if err != nil {
		c.JSON(400, gin.H{"success": false, "error": "INVALID_ATTACHMENTS", "message": err.Error()})
		return
	}
	hasImageAttachments := len(chatAttachments) > 0
	imageOCRContext := buildChatImageOCRContext(chatAttachments)
	historyQuestion := buildChatHistoryQuestion(req.Question, chatAttachments)

	userMessageID := uuid.New().String()
	userMsg := gin.H{
		"id":        userMessageID,
		"role":      "user",
		"content":   req.Question,
		"timestamp": time.Now().Format(time.RFC3339),
	}
	if hasImageAttachments {
		userMsg["attachments"] = chatImageAttachmentsForMessage(chatAttachments)
	}
	userMsgBytes, _ := json.Marshal(userMsg)
	userErr := appendChatHistoryMessage(config.Ctx, req.SessionID, string(userMsgBytes))

	if userErr != nil {
		log.Printf("❌ [DB Error] Failed to save user message: %v", userErr)
	} else {
		log.Printf("💾 [DB Chat] Saved user message to session: %s", req.SessionID)
	}

	// 1. Setup SSE headers - Cực kỳ quan trọng cho Streaming
	if userErr == nil && hasImageAttachments {
		updateChatImageAttachmentMessageID(config.Ctx, userID, req.SessionID, userMessageID, chatAttachments)
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(500, gin.H{"error": "Streaming unsupported"})
		return
	}
	writeChatStatus(c, flusher, "thinking")
	if hasImageAttachments {
		writeChatInsight(c, flusher, fmt.Sprintf("Da nhan OCR tu %d anh dinh kem va se dung lam ngu canh bo sung.", len(chatAttachments)))
	}
	writeChatInsight(c, flusher, fmt.Sprintf("Đã nhận câu hỏi: \"%s\".", compactChatPreview(req.Question, 120)))

	// [Phase C] Keyword pre-filter: bắt social messages rõ ràng trước mọi DB/Redis/API call
	if utils.IsSensitiveContent(req.Question) {
		log.Printf("⚡ [CHAT] Keyword pre-filter: sensitive content detected for session=%s q=%q", req.SessionID, req.Question)
		sensitiveMsg := "Xin lỗi, tôi không tìm thấy nội dung liên quan đến câu hỏi này trong tài liệu. Hãy thử hỏi về các nội dung được đề cập trong tài liệu."
		sendHardcodedSSEResponse(c, flusher, req.SessionID, sensitiveMsg)
		go saveHardcodedToHistory(req.SessionID, historyQuestion, sensitiveMsg)
		return
	}

	if isObviouslyOffTopic(req.Question) && !hasImageAttachments {
		log.Printf("⚡ [CHAT] Keyword pre-filter: obvious off-topic for session=%s q=%q", req.SessionID, req.Question)
		reply := sendSoftRejectSSE(c, flusher, req.SessionID, req.Question, targetTitle)
		go saveHardcodedToHistory(req.SessionID, historyQuestion, reply)
		return
	}

	// [Cache Check] Kiểm tra answer cache trước mọi API call (chỉ cho non-thinking mode)
	if !thinkingMode && !hasImageAttachments {
		scopeID := req.DocumentID
		if req.CollectionID != "" {
			scopeID = req.CollectionID
		}
		cacheKey := utils.AnswerCacheKey(req.Question, scopeID, userPersona)
		if cached, ok := utils.GetAnswerCache(context.Background(), cacheKey); ok {
			log.Printf("🎯 [CHAT] Cache HIT session=%s scope=%s", req.SessionID, scopeID)
			writeChatInsight(c, flusher, "Đã tìm thấy câu trả lời trong bộ nhớ đệm, trả về kết quả ngay lập tức.")
			sendCachedSSEResponse(c, flusher, req.SessionID, cached)
			go saveHardcodedToHistory(req.SessionID, historyQuestion, cached.Answer)
			return
		}
	}

	if req.CollectionID != "" {
		writeChatInsight(c, flusher, "Đang đọc ngữ cảnh từ bộ tài liệu và lịch sử hội thoại gần nhất.")
	} else {
		writeChatInsight(c, flusher, "Đang đọc ngữ cảnh từ tài liệu đang mở và lịch sử hội thoại gần nhất.")
	}

	// 2. Lấy lịch sử từ Redis (Cơ chế Resilience - Nếu Redis lỗi vẫn chạy tiếp)
	var historySummary string
	if config.RedisClient != nil {
		historyKey := "session:" + req.SessionID
		// Timeout ngắn cho Redis để không block request chính
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		rawHistory, err := config.RedisClient.LRange(ctx, historyKey, -3, -1).Result()
		if err == nil {
			for _, item := range rawHistory {
				var qa QAHistory
				if err := json.Unmarshal([]byte(item), &qa); err == nil {
					historySummary += fmt.Sprintf("Human: %s\nAssistant: %s\n", qa.Question, qa.Answer)
				}
			}
		} else {
			log.Printf("⚠️ [CHAT] Redis History Error (Skipping): %v", err)
		}
	}

	// Dynamic Branding: Chỉ chào trang trọng ở câu đầu tiên của phiên chat
	isFirstMessage := (historySummary == "")
	systemPrompt = utils.ApplyMindexBranding(systemPrompt, isFirstMessage)
	if isFirstMessage {
		writeChatInsight(c, flusher, "Đây là lượt hỏi đầu trong phiên, AI sẽ trả lời trực tiếp theo ngữ cảnh hiện có.")
	} else {
		writeChatInsight(c, flusher, "Đã tìm thấy lịch sử hội thoại, AI sẽ dùng nó để hiểu câu hỏi nối tiếp.")
	}

	// 3. Query Rewrite (SYS-023) - Làm rõ câu hỏi dựa trên lịch sử trước khi search
	// Strip social noise trước — chỉ dùng cho embedding/search, không thay req.Question gốc.
	cleanQuestion := utils.StripSocialLeadin(req.Question)
	searchQuery := utils.RewriteQueryWithHistory(cleanQuestion, historySummary)
	if strings.TrimSpace(searchQuery) != "" && searchQuery != req.Question {
		writeChatInsight(c, flusher, fmt.Sprintf("Đã diễn giải lại câu hỏi để tìm kiếm chính xác hơn: \"%s\".", compactChatPreview(searchQuery, 140)))
	}

	// Intent classification (Fix 6) — zero LLM, thuần heuristic
	intent := utils.ClassifyIntent(req.Question, historySummary)
	log.Printf("🎯 [CHAT] Intent=%s session=%s", intent, req.SessionID)
	switch intent {
	case utils.IntentConversational:
		writeChatInsight(c, flusher, "Đã nhận câu hỏi kèm tiền tố hội thoại, xử lý phần câu hỏi thực.")
	case utils.IntentTangential:
		writeChatInsight(c, flusher, "Câu hỏi là follow-up trong phiên — tìm kiếm có tham chiếu lịch sử.")
	}

	// 4. Vector Embed — chiến lược thay đổi theo intent
	// IntentOpinion → HyDE (Fix 5): embed "câu trả lời giả" thay vì câu hỏi trực tiếp
	// để đưa queryVec về "answer space" gần với document chunk embeddings hơn.
	// BM25 vẫn dùng searchQuery (keyword-based, không benefit từ HyDE).
	embedSource := searchQuery
	if intent == utils.IntentOpinion {
		writeChatInsight(c, flusher, "Câu hỏi dạng ý kiến/đánh giá — đang tạo ngữ cảnh tài liệu giả để tìm kiếm chính xác hơn (HyDE).")
		embedSource = utils.GenerateHyDE(searchQuery)
	}

	writeChatInsight(c, flusher, "Đang tạo embedding để tìm các đoạn tài liệu liên quan.")
	queryVec, err := utils.GeminiEmbedPool.EmbedWithRetry(embedSource, utils.CallGeminiAPI)
	if err != nil {
		fmt.Fprintf(c.Writer, "event: error\ndata: {\"error\": \"EMBEDDING_FAILED\"}\n\n")
		flusher.Flush()
		return
	}

	// 5. Hybrid Search (Vector + BM25)
	// queryVec: từ HyDE nếu IntentOpinion, từ searchQuery nếu không
	// searchQuery: luôn là clean query gốc cho BM25 keyword matching
	log.Printf("🔍 [CHAT] Performing Hybrid Search for session: %s (Query: %s, Intent: %s)", req.SessionID, searchQuery, intent)
	searchResults, maxVectorSim, _ := utils.HybridSearch(req.DocumentID, req.CollectionID, searchQuery, queryVec, 8)
	log.Printf("🎯 [CHAT] Hybrid Search done: results=%d, maxVectorSim=%.4f, session=%s", len(searchResults), maxVectorSim, req.SessionID)
	writeChatInsight(c, flusher, fmt.Sprintf("Hybrid search tìm thấy %d đoạn ứng viên trong tài liệu.", len(searchResults)))

	// Fix 7: Session topic vector check — trước mọi off-topic judgment
	// Tải topic vector của phiên từ Redis, tính cosine sim với queryVec.
	// Nếu cao → câu hỏi khớp với chủ đề session dù không khớp document chunk cụ thể.
	simToTopic := utils.GetSessionTopicSim(context.Background(), req.SessionID, queryVec)
	if simToTopic > 0 {
		log.Printf("🗺️ [CHAT] Session topic sim=%.4f session=%s", simToTopic, req.SessionID)
	}

	// Short-circuit: off-topic theo similarity threshold
	// Ngoại lệ: nếu câu hỏi có heuristic web trigger (giá, luật, thời sự...)
	// → vẫn cho tiếp tục để web search có cơ hội trả lời
	isOffTopic := len(searchResults) == 0 || maxVectorSim < offTopicVectorSimThreshold

	// Session topic bypass (Fix 7): câu hỏi không khớp doc nhưng khớp chủ đề session
	if isOffTopic && simToTopic >= utils.TopicSimBypassThreshold {
		isOffTopic = false
		log.Printf("🗺️ [CHAT] Topic-vec bypass: simToTopic=%.4f >= %.2f, session=%s", simToTopic, utils.TopicSimBypassThreshold, req.SessionID)
		writeChatInsight(c, flusher, fmt.Sprintf("Câu hỏi liên quan đến chủ đề phiên hội thoại (sim=%.2f) — tiếp tục xử lý.", simToTopic))
	}

	// History-aware bypass: câu hỏi borderline (0.40–threshold) trong phiên đang có lịch sử
	// → không block vì khả năng cao là follow-up hợp lệ về cùng chủ đề
	if isOffTopic && historySummary != "" && maxVectorSim >= 0.40 {
		isOffTopic = false
		log.Printf("🔄 [CHAT] History-aware bypass: maxSim=%.4f (borderline), session has history → continuing, session=%s", maxVectorSim, req.SessionID)
		writeChatInsight(c, flusher, "Câu hỏi có độ tương đồng thấp nhưng phiên hội thoại đang có lịch sử liên quan, tiếp tục xử lý.")
	}

	// flexRuleInjection: thêm vào systemPrompt sau SYS-013 khi dùng soft fallback
	var flexRuleInjection string

	if isOffTopic {
		hasWebTrigger := config.Env.WebSearchEnabled && utils.WebSearchHeuristicTriggered(req.Question, searchQuery)
		if !hasImageAttachments && !hasWebTrigger {
			if historySummary != "" {
				// Phiên đang có lịch sử nhưng sim < 0.40 → soft fallback thay vì hard block
				// AI được dùng kiến thức chung có liên quan, ghi rõ nguồn gốc
				log.Printf("🟡 [CHAT] Soft fallback (has history, sim=%.4f < 0.40): session=%s", maxVectorSim, req.SessionID)
				writeChatInsight(c, flusher, "Không tìm thấy nội dung khớp trong tài liệu, AI sẽ trả lời dựa trên kiến thức chung có liên quan.")
				flexRuleInjection = `

[FLEX RESPONSE RULE]
Câu hỏi hiện tại không có nội dung khớp trong tài liệu (similarity thấp).
Bạn ĐƯỢC PHÉP dùng kiến thức chung để trả lời NẾU câu hỏi liên quan đến chủ đề tài liệu hoặc chủ đề đang thảo luận.
Khi dùng kiến thức ngoài tài liệu, thêm chú thích "(Kiến thức chung)" ở cuối đoạn đó.
Tuyệt đối không bịa số trang, không hallucinate trích dẫn.
Nếu hoàn toàn không liên quan, lịch sự giải thích phạm vi hỗ trợ.`
			} else {
				// Phiên mới, không có history → soft reject với AI-generated response
				log.Printf("⚡ [CHAT] Off-topic short-circuit: results=%d, maxSim=%.4f, threshold=%.2f, session=%s", len(searchResults), maxVectorSim, offTopicVectorSimThreshold, req.SessionID)
				reply := sendSoftRejectSSE(c, flusher, req.SessionID, req.Question, targetTitle)
				go saveHardcodedToHistory(req.SessionID, historyQuestion, reply)
				return
			}
		}
		if hasImageAttachments {
			log.Printf("[CHAT] Off-topic document score bypassed because image OCR context exists: maxSim=%.4f session=%s", maxVectorSim, req.SessionID)
			writeChatInsight(c, flusher, "Cau hoi co anh dinh kem, bo qua chan off-topic theo tai lieu de doc OCR.")
		}
		if hasWebTrigger {
			log.Printf("🌐 [CHAT] Off-topic but web trigger detected, allowing web search: maxSim=%.4f session=%s", maxVectorSim, req.SessionID)
			writeChatInsight(c, flusher, "Nội dung không có trong tài liệu, sẽ thử tìm kiếm web để bổ sung.")
		}
	}
	// Fix 7: Cập nhật session topic vector async — chỉ sau khi câu hỏi pass off-topic check
	go utils.UpdateSessionTopicVec(context.Background(), req.SessionID, queryVec)

	var contextText string
	var sources []map[string]interface{}
	for _, res := range searchResults {
		if req.CollectionID != "" {
			contextText += fmt.Sprintf("📄 Tài liệu: %s\nĐoạn %d (Trang %d): %s\n\n", res.DocTitle, res.ChunkIndex, res.PageNumber, res.RetrievalContent)
		} else {
			contextText += fmt.Sprintf("Đoạn %d (Trang %d): %s\n\n", res.ChunkIndex, res.PageNumber, res.RetrievalContent)
		}

		sources = append(sources, map[string]interface{}{
			"type":        "document",
			"chunk_index": res.ChunkIndex,
			"page":        res.PageNumber,
			"page_number": res.PageNumber,
			"score":       res.Score,
			"similarity":  res.Score,
			"content":     res.RetrievalContent,
			"doc_title":   res.DocTitle,
		})
	}

	if imageOCRContext != "" {
		if strings.TrimSpace(contextText) == "" {
			contextText = imageOCRContext
		} else {
			contextText = imageOCRContext + "\n\n" + contextText
		}
	}

	// 5. Build prompt & Call AI
	log.Printf("🤖 RAG: Context Length: %d chars", len(contextText))
	if strings.TrimSpace(contextText) == "" {
		writeChatInsight(c, flusher, "Không tìm thấy đoạn tài liệu đủ liên quan, AI sẽ dùng fallback theo persona.")
	} else {
		writeChatInsight(c, flusher, fmt.Sprintf("Đã gom %d nguồn trích dẫn vào ngữ cảnh trả lời.", len(sources)))
	}

	var webSearchMeta gin.H
	if config.Env.WebSearchEnabled {
		webPlan := utils.DecideWebSearch(req.Question, searchQuery, contextText, docIntelStr, userPersona, userTier, thinkingMode)
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
				log.Printf("⚠️ [CHAT] Web search user limit reached user=%s tier=%s retry_after=%s", userID, userTier, retryAfter)
				webSearchMeta["used"] = false
				webSearchMeta["rate_limited"] = true
				webSearchMeta["retry_after_seconds"] = int(retryAfter.Seconds())
				webSearchMeta["error"] = "web search user rate limit reached"
				writeChatInsight(c, flusher, "Web search bị giới hạn lượt dùng, AI tiếp tục với ngữ cảnh tài liệu hiện có.")
			} else {
				writeChatStatus(c, flusher, "searching")
				writeChatInsight(c, flusher, fmt.Sprintf("Đang tìm web với truy vấn: \"%s\".", compactChatPreview(webPlan.Query, 140)))
				webCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				webResp, err := utils.SearchWeb(webCtx, webPlan)
				cancel()
				writeChatStatus(c, flusher, "thinking")
				if err != nil {
					log.Printf("⚠️ [CHAT] Web search skipped after decision: %v", err)
					webSearchMeta["error"] = err.Error()
					writeChatInsight(c, flusher, "Web search không khả dụng, AI tiếp tục dựa trên tài liệu và ngữ cảnh hiện có.")
				} else {
					webContext := utils.FormatWebSearchContext(webResp)
					if webContext != "" {
						contextText += "\n\n" + webContext
						sources = append(sources, utils.WebSearchSources(webResp)...)
						systemPrompt += buildWebSearchPromptRules(userPersona, thinkingMode)
						webSearchMeta["used"] = true
						webSearchMeta["provider"] = webResp.Provider
						webSearchMeta["from_cache"] = webResp.FromCache
						webSearchMeta["results"] = len(webResp.Results)
						log.Printf("🌐 [CHAT] Web search used provider=%s cache=%t results=%d", webResp.Provider, webResp.FromCache, len(webResp.Results))
						writeChatInsight(c, flusher, fmt.Sprintf("Đã bổ sung %d kết quả web từ %s.", len(webResp.Results), webResp.Provider))
					}
				}
			}
		} else {
			log.Printf("🌐 [CHAT] Web search decision=false reason=%s", webPlan.Reason)
			writeChatInsight(c, flusher, "Không cần web search vì ngữ cảnh tài liệu đã đủ cho câu hỏi này.")
		}
	} else if req.Thinking {
		webSearchMeta = gin.H{
			"requested": true,
			"thinking":  thinkingMode,
			"used":      false,
			"reason":    "web search disabled",
		}
		writeChatInsight(c, flusher, "Web search đang tắt, AI sẽ trả lời trong phạm vi tài liệu và ngữ cảnh hiện có.")
	}

	if thinkingMode {
		systemPrompt += `

[THINKING MODE]
User đã bật chế độ Thinking cho câu hỏi này. Hãy phân tích kỹ hơn, kiểm tra mâu thuẫn giữa tài liệu và nguồn bổ sung nếu có, và trả lời có cấu trúc rõ ràng. Không bịa nguồn.`
	}

	// Fallback logic (SYS-013): Nếu không có context, dùng prompt chuyên biệt để trả lời lịch sự
	if contextText == "" {
		systemPrompt = personaCfg.PromptNoContext
		if systemPrompt == "" {
			systemPrompt = "Xin lỗi, tôi không tìm thấy dữ liệu trong tài liệu gốc. Có vẻ như tài liệu đã bị xóa hoặc hết hạn."
		}
		log.Printf("⚠️ [CHAT] No context found. Using SYS-013 fallback prompt.")
	}
	// Append flex rule sau SYS-013 để không bị overwrite
	if flexRuleInjection != "" {
		systemPrompt += flexRuleInjection
	}

	finalPrompt := buildRAGPrompt(systemPrompt, historySummary, contextText, req.Question)
	log.Printf("🤖 Prompt Length: %d chars", len(finalPrompt))
	messages := []utils.ChatMessage{
		{Role: "system", Content: finalPrompt},
		{Role: "user", Content: req.Question},
	}

	// 6. Gửi sang AI Orchestrator (Tự động quản lý NineRouter -> Groq -> Cerebras -> Mistral -> OpenRouter)
	log.Printf("🚀 [CHAT] Đang gửi yêu cầu tới AI Orchestrator cho session: %s", req.SessionID)
	writeChatInsight(c, flusher, "Đang gửi prompt đã dựng sang AI Orchestrator để bắt đầu sinh câu trả lời.")
	chatStart := time.Now()

	fullAnswer, usedProvider, streamErr := utils.AI.ChatStream(utils.ServiceChat, c, messages, req.Model)

	chatLatency := int(time.Since(chatStart).Milliseconds())

	// [Cache Write] Lưu câu trả lời vào cache sau khi AI thành công (async, không block SSE)
	if !thinkingMode && !hasImageAttachments && streamErr == nil && fullAnswer != "" {
		scopeID := req.DocumentID
		if req.CollectionID != "" {
			scopeID = req.CollectionID
		}
		cacheKey := utils.AnswerCacheKey(req.Question, scopeID, userPersona)
		go utils.SetAnswerCache(context.Background(), cacheKey, fullAnswer, sources, utils.AnswerCacheTTLDoc)
	}

	// Ghi AI Response Log:
	// Tạo logID ngay (ko async) để event done có thể gửi log_id về FE,
	// thực sự ghi DB chạy async để không block SSE.
	var logID string
	if streamErr == nil && fullAnswer != "" {
		logID = uuid.New().String() // Generate UUID trước
		entry := AIResponseLogEntry{
			ID:           logID, // Truyền UUID vào để dùng làm PK
			SessionID:    req.SessionID,
			UserID:       userID,
			DocumentID:   req.DocumentID,
			CollectionID: req.CollectionID,
			Question:     req.Question,
			Answer:       fullAnswer,
			ModelUsed:    string(usedProvider),
			LatencyMs:    chatLatency,
			TokenCount:   (len(finalPrompt) + len(fullAnswer)) / 4,
			SourcesCount: len(sources),
		}
		go SaveAIResponseLogWithID(entry) // Lưu DB async, ID đã biết trước
	}

	// Log token usage (approximate)
	go func() {
		status := "ok"
		if streamErr != nil {
			status = "error"
		}
		utils.LogTokenUsage(utils.TokenUsageLog{
			UserID: &userID,
			DocumentID: func() *string {
				s := req.DocumentID
				if s == "" {
					return nil
				}
				return &s
			}(),
			SessionID:   req.SessionID,
			Service:     string(usedProvider),
			Operation:   "chat",
			TotalTokens: (len(finalPrompt) + len(fullAnswer)) / 4,
			LatencyMs:   chatLatency,
			KeyAlias:    "auto_fallback",
			Status:      status,
		})
	}()

	if streamErr != nil {
		log.Printf("❌ [CHAT] Tất cả AI providers đều thất bại: %v", streamErr)
		fmt.Fprintf(c.Writer, "event: error\ndata: {\"error\": \"AI_SERVICE_DOWN\"}\n\n")
		flusher.Flush()
		return
	}
	writeChatInsight(c, flusher, fmt.Sprintf("Hoàn tất sinh câu trả lời bằng provider %s trong %.1fs.", usedProvider, float64(chatLatency)/1000))

	// 7. Kết thúc file event done
	donePayload := gin.H{
		"session_id": req.SessionID,
		"message_id": uuid.New().String(),
		"log_id":     logID, // Dùng cho thumbs up/down UI
		"answer":     fullAnswer,
		"sources":    sources,
		"web_search": webSearchMeta,
	}
	doneBytes, _ := json.Marshal(donePayload)
	fmt.Fprintf(c.Writer, "event: done\ndata: %s\n\n", string(doneBytes))
	flusher.Flush()

	// 8. Lưu lịch sử vào Redis & PostgreSQL Background
	if config.RedisClient != nil || config.DB != nil {
		go func() {
			// Redis (Short-term cache)
			if config.RedisClient != nil {
				historyKey := "session:" + req.SessionID
				newQA := QAHistory{Question: historyQuestion, Answer: fullAnswer}
				qaBytes, _ := json.Marshal(newQA)
				config.RedisClient.RPush(context.Background(), historyKey, string(qaBytes))
				config.RedisClient.LTrim(context.Background(), historyKey, -10, -1)
				config.RedisClient.Expire(context.Background(), historyKey, 1*time.Hour)
			}

			// PostgreSQL (Long-term storage)
			if config.DB != nil {
				assistantMsg := gin.H{
					"id":        uuid.New().String(),
					"role":      "assistant",
					"content":   fullAnswer,
					"sources":   sources,
					"timestamp": time.Now().Format(time.RFC3339),
					"log_id":    logID,
				}
				asstMsgBytes, _ := json.Marshal(assistantMsg)
				err := appendChatHistoryMessage(context.Background(), req.SessionID, string(asstMsgBytes))

				if err != nil {
					log.Printf("❌ [DB Error] Failed to save assistant message: %v", err)
				}

				if req.CollectionID != "" {
					config.DB.Exec(context.Background(), `UPDATE collections SET last_chat_at=NOW() WHERE id=$1`, req.CollectionID)
				}
			}
		}()
	}
}

func getCollectionDocumentIDsHelper(colID string) ([]string, error) {
	rows, err := config.DB.Query(context.Background(), `
		SELECT document_id FROM collection_documents WHERE collection_id = $1`, colID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, nil
}

func appendChatHistoryMessage(ctx context.Context, sessionID string, messageJSON string) error {
	_, err := config.DB.Exec(ctx, `
		UPDATE chat_histories
		SET full_messages = (
				SELECT COALESCE(jsonb_agg(value ORDER BY ord), '[]'::jsonb)
				FROM (
					SELECT value, ord
					FROM jsonb_array_elements(COALESCE(full_messages, '[]'::jsonb) || $1::jsonb) WITH ORDINALITY AS e(value, ord)
					ORDER BY ord DESC
					LIMIT $3
				) kept
			),
			message_count = message_count + 1
		WHERE session_id = $2`, messageJSON, sessionID, maxStoredChatMessages)
	return err
}

func buildRAGPrompt(sys, hist, ctx, curr string) string {
	return fmt.Sprintf(`%s
	
[CONVERSATION HISTORY]
%s

[CONTEXT]
%s

[CÂU HỎI HIỆN TẠI]
%s
`, sys, hist, ctx, curr)
}

func writeChatStatus(c *gin.Context, flusher http.Flusher, status string) {
	payload, _ := json.Marshal(gin.H{"status": status})
	fmt.Fprintf(c.Writer, "event: status\ndata: %s\n\n", string(payload))
	flusher.Flush()
}

func writeChatInsight(c *gin.Context, flusher http.Flusher, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	payload, _ := json.Marshal(gin.H{"text": text})
	fmt.Fprintf(c.Writer, "event: insight\ndata: %s\n\n", string(payload))
	flusher.Flush()
}

// writeChatTokens sends a complete answer as word-chunked SSE "token"
// events, for responses produced by the AI Tool Framework dispatcher
// (non-streaming function-calling loop) rather than the streaming adapter.
// Known trade-off: time-to-first-token equals total generation time here,
// since the full answer exists before any token is sent.
func writeChatRichContent(c *gin.Context, flusher http.Flusher, richContent json.RawMessage) {
	if len(richContent) == 0 {
		return
	}
	fmt.Fprintf(c.Writer, "event: rich_content\ndata: %s\n\n", string(richContent))
	flusher.Flush()
}

func writeChatTokens(c *gin.Context, flusher http.Flusher, text string) {
	words := strings.Fields(text)
	for i, w := range words {
		token := w
		if i < len(words)-1 {
			token += " "
		}
		tokenPayload, _ := json.Marshal(map[string]string{"token": token})
		fmt.Fprintf(c.Writer, "event: token\ndata: %s\n\n", string(tokenPayload))
		flusher.Flush()
	}
}

func compactChatPreview(text string, limit int) string {
	preview := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	runes := []rune(preview)
	if limit > 0 && len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return preview
}

func getUserTierForChat(userID string, role string) string {
	if role == "admin" {
		return "ADMIN"
	}
	var tier string
	err := config.DB.QueryRow(config.Ctx, `SELECT COALESCE(tier, 'FREE') FROM users WHERE id = $1`, userID).Scan(&tier)
	if err != nil || strings.TrimSpace(tier) == "" {
		return "FREE"
	}
	return strings.ToUpper(strings.TrimSpace(tier))
}

func isTierAllowedThinking(tier string) bool {
	tier = strings.ToUpper(strings.TrimSpace(tier))
	return tier == "PRO" || tier == "ULTRA" || tier == "ADMIN"
}

func sendHardcodedSSEResponse(c *gin.Context, flusher http.Flusher, sessionID, msg string) {
	writeChatInsight(c, flusher, "Không tìm thấy nội dung liên quan, trả lời theo quy tắc hệ thống.")
	words := strings.Fields(msg)
	for i, w := range words {
		token := w
		if i < len(words)-1 {
			token += " "
		}
		tokenPayload, _ := json.Marshal(map[string]string{"token": token})
		fmt.Fprintf(c.Writer, "event: token\ndata: %s\n\n", string(tokenPayload))
		flusher.Flush()
	}
	donePayload, _ := json.Marshal(gin.H{
		"session_id": sessionID,
		"message_id": uuid.New().String(),
		"answer":     msg,
		"sources":    []interface{}{},
	})
	fmt.Fprintf(c.Writer, "event: done\ndata: %s\n\n", string(donePayload))
	flusher.Flush()
}

// sendSoftRejectSSE gọi AI để tạo câu từ chối thân thiện, có ngữ cảnh tài liệu,
// thay vì trả về chuỗi hardcoded. Fallback về chuỗi mặc định nếu AI không khả dụng.
// Trả về nội dung đã stream để caller có thể lưu vào history.
func sendSoftRejectSSE(c *gin.Context, flusher http.Flusher, sessionID, question, docTitle string) string {
	const fallback = "Tôi chỉ có thể hỗ trợ các câu hỏi liên quan đến nội dung tài liệu. Bạn có muốn hỏi về nội dung này không?"

	title := strings.TrimSpace(docTitle)
	if title == "" {
		title = "tài liệu hiện tại"
	}

	var reply string
	if utils.AI != nil {
		sysPrompt := fmt.Sprintf(`Bạn là Mindex AI, trợ lý học tập đang hỗ trợ tài liệu "%s".
Người dùng vừa gửi tin nhắn nằm ngoài phạm vi tài liệu.
Trả lời đúng 1-2 câu: từ chối nhẹ nhàng, không cứng nhắc, gợi ý hướng câu hỏi phù hợp về tài liệu.
Không lặp lại câu hỏi của user. Tone thân thiện, tự nhiên.`, title)

		msgs := []utils.ChatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: question},
		}
		r, _, err := utils.AI.ChatNonStream(utils.ServiceSearch, msgs)
		if err == nil && strings.TrimSpace(r) != "" {
			reply = strings.TrimSpace(r)
		} else {
			log.Printf("⚠️ [SoftReject] AI failed: %v. Using fallback.", err)
		}
	}
	if reply == "" {
		reply = fallback
	}

	sendHardcodedSSEResponse(c, flusher, sessionID, reply)
	return reply
}

func sendCachedSSEResponse(c *gin.Context, flusher http.Flusher, sessionID string, cached *utils.CachedAnswer) {
	words := strings.Fields(cached.Answer)
	for i, w := range words {
		token := w
		if i < len(words)-1 {
			token += " "
		}
		tokenPayload, _ := json.Marshal(map[string]string{"token": token})
		fmt.Fprintf(c.Writer, "event: token\ndata: %s\n\n", string(tokenPayload))
		flusher.Flush()
	}
	donePayload, _ := json.Marshal(gin.H{
		"session_id": sessionID,
		"message_id": uuid.New().String(),
		"answer":     cached.Answer,
		"sources":    cached.Sources,
		"from_cache": true,
	})
	fmt.Fprintf(c.Writer, "event: done\ndata: %s\n\n", string(donePayload))
	flusher.Flush()
}

func saveHardcodedToHistory(sessionID, question, answer string) {
	asstMsg := gin.H{
		"id":        uuid.New().String(),
		"role":      "assistant",
		"content":   answer,
		"sources":   []interface{}{},
		"timestamp": time.Now().Format(time.RFC3339),
	}
	asstMsgBytes, _ := json.Marshal(asstMsg)
	if err := appendChatHistoryMessage(context.Background(), sessionID, string(asstMsgBytes)); err != nil {
		log.Printf("❌ [DB Error] Failed to save hardcoded response: %v", err)
	}
	if config.RedisClient != nil {
		historyKey := "session:" + sessionID
		qa := QAHistory{Question: question, Answer: answer}
		qaBytes, _ := json.Marshal(qa)
		config.RedisClient.RPush(context.Background(), historyKey, string(qaBytes))
		config.RedisClient.LTrim(context.Background(), historyKey, -10, -1)
		config.RedisClient.Expire(context.Background(), historyKey, 1*time.Hour)
	}
}

// isObviouslyOffTopic phát hiện nhanh các tin nhắn xã giao rõ ràng không phải câu hỏi
// về tài liệu. Chỉ dùng string operations — zero DB/Redis/API call.
// Nguyên tắc: chỉ bắt những gì 100% CHẮC CHẮN off-topic trong mọi ngữ cảnh tài liệu.
func isObviouslyOffTopic(question string) bool {
	q := utils.RemoveVietnameseSigns(strings.TrimSpace(question))

	// Bỏ dấu câu cuối để "ok." == "ok"
	q = strings.TrimRight(q, ".!?,;:")
	q = strings.Join(strings.Fields(q), " ")

	exactBlocks := []string{
		// Chào hỏi
		"hi", "hey", "hello", "xin chao", "chao",
		// Cảm ơn / phản hồi ngắn
		"cam on", "cam on ban", "cam on nhe", "thanks", "thank you", "thank u",
		// Đồng ý / thừa nhận
		"ok", "oke", "okie", "okay", "duoc", "duoc roi", "on roi",
		"vang", "vang a", "vang ah", "da", "da a", "roi", "nhe",
		// Biểu cảm rỗng
		"hm", "hmm", "uh", "uh huh", "ah", "oh", "a",
	}
	for _, exact := range exactBlocks {
		if q == exact {
			return true
		}
	}

	prefixBlocks := []string{
		// Chào kiểu "xin chào bạn", "chào buổi sáng"
		"xin chao ", "chao ban", "chao buoi",
		// Câu hỏi về danh tính AI
		"ban la ai", "ban ten gi", "may la ai", "mi la ai",
		"ai day", "may la gi", "bot la gi", "ai la ban",
		// Lời tạm biệt
		"tam biet", "bye", "goodbye",
	}
	for _, prefix := range prefixBlocks {
		if strings.HasPrefix(q, prefix) {
			return true
		}
	}
	return false
}

func buildWebSearchPromptRules(userPersona string, thinkingMode bool) string {
	rules := `

[WEB SEARCH RULES]
Bạn có thêm [WEB SEARCH CONTEXT] từ nguồn web bên ngoài tài liệu.
1. Chỉ dùng web context để xác minh hoặc bổ sung thông tin liên quan đến tài liệu/câu hỏi.
2. Khi dùng web context, nêu nguồn bằng tên trang hoặc URL ngắn. Không trình bày web context như thể nó nằm trong tài liệu gốc.
3. Nếu web context mâu thuẫn với tài liệu, nêu rõ phần nào từ tài liệu và phần nào từ web.
4. Nếu nguồn web không đủ chắc, nói rõ mức độ không chắc chắn.`

	if userPersona == "doctor" || userPersona == "legal" {
		rules += `
5. Với nội dung y khoa/pháp lý: chỉ trả lời trong phạm vi liên quan đến tài liệu; không biến web search thành tư vấn chuyên môn thay thế bác sĩ/luật sư hoặc văn bản chính thức.`
	}
	if thinkingMode {
		rules += `
6. Thinking mode đang bật: ưu tiên kiểm chứng kỹ và giải thích ngắn gọn vì sao nguồn web có liên quan.`
	}
	return rules
}
