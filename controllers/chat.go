package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"mindex-backend/config"
	"mindex-backend/internal/persona"
	"mindex-backend/utils"
	"net/http"
	"time"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ChatRequest struct {
	DocumentID    string `json:"document_id"`
	CollectionID  string `json:"collection_id"`
	SessionID     string `json:"session_id"`
	Question      string `json:"question" binding:"required"`
	UseSystemDocs bool   `json:"use_system_docs"`
	ForkID        string `json:"fork_id"` // ID của shared_link nếu đây là fork session
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

	log.Printf("📥 [CHAT] [User: %s] [Doc: %s] [Session: %s] Question: %s", userID, req.DocumentID, req.SessionID, req.Question)
	
	// --- MỚI: KIỂM TRA HẾT HẠN (EXPIRED CHECK) ---
	var expiredAt *time.Time
	var targetTitle string
	if req.CollectionID != "" {
		err := config.DB.QueryRow(config.Ctx, "SELECT name, expired_at FROM collections WHERE id = $1", req.CollectionID).Scan(&targetTitle, &expiredAt)
		if err != nil {
			c.JSON(404, gin.H{"success": false, "error": "NOT_FOUND", "message": "Bộ tài liệu không tồn tại"})
			return
		}
	} else if req.DocumentID != "" {
		err := config.DB.QueryRow(config.Ctx, "SELECT title, expired_at FROM documents WHERE id = $1", req.DocumentID).Scan(&targetTitle, &expiredAt)
		if err != nil {
			c.JSON(404, gin.H{"success": false, "error": "NOT_FOUND", "message": "Tài liệu không tồn tại"})
			return
		}
	}

	if expiredAt != nil && expiredAt.Before(time.Now()) {
		log.Printf("🚫 [CHAT] Access Denied: %s has expired at %v", targetTitle, expiredAt)
		c.JSON(403, gin.H{
			"success": false, 
			"error": "EXPIRED", 
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

NGUYÊN TẮC TRÍCH DẪN NGUỒN (bắt buộc):
Mỗi thông tin phải ghi rõ nguồn theo format:
  → '(Tên tài liệu, Trang X)'
NGHIÊM CẤM sử dụng từ 'Chunk' hoặc các thuật ngữ kỹ thuật tương tự trong câu trả lời. 
Nếu không có tên tài liệu, hãy dùng logic nội dung để gọi tên (ví dụ: 'Theo tài liệu về...') thay vì dùng ID hay Index.

NGUYÊN TẮC TRẢ LỜI:
1. Chỉ trả lời dựa trên [CONTEXT] được cung cấp từ bộ tài liệu.
2. Nếu các tài liệu ĐỒNG THUẬN về 1 điểm -> tổng hợp và ghi tất cả nguồn.
3. Nếu các tài liệu KHÁC NHAU về 1 điểm -> trình bày song song: 'Theo [File A]: ... | Theo [File B]: ...'
4. Ưu tiên thông tin xuất hiện trong NHIỀU tài liệu hơn.
5. Khi so sánh giữa các tài liệu, hãy dùng cấu trúc bảng hoặc danh sách song song.`
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
	userMsg := gin.H{
		"id":        uuid.New().String(),
		"role":      "user",
		"content":   req.Question,
		"timestamp": time.Now().Format(time.RFC3339),
	}
	userMsgBytes, _ := json.Marshal(userMsg)
	_, userErr := config.DB.Exec(config.Ctx, `
		UPDATE chat_histories 
		SET full_messages = full_messages || $1::jsonb, 
		    message_count = message_count + 1 
		WHERE session_id = $2`, string(userMsgBytes), req.SessionID)
	
	if userErr != nil {
		log.Printf("❌ [DB Error] Failed to save user message: %v", userErr)
	} else {
		log.Printf("💾 [DB Chat] Saved user message to session: %s", req.SessionID)
	}

	// 1. Setup SSE headers - Cực kỳ quan trọng cho Streaming
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(500, gin.H{"error": "Streaming unsupported"})
		return
	}

	// 2. Lấy lịch sử từ Redis (Cơ chế Resilience - Nếu Redis lỗi vẫn chạy tiếp)
	var historySummary string
	if config.RedisClient != nil {
		historyKey := "session:" + req.SessionID
		// Timeout ngắn cho Redis để không block request chính
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		
		rawHistory, err := config.RedisClient.LRange(ctx, historyKey, 0, -1).Result()
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

	// 3. Vector Embed câu hỏi
	queryVec, err := utils.GeminiEmbedPool.EmbedWithRetry(req.Question, utils.CallGeminiAPI)
	if err != nil {
		fmt.Fprintf(c.Writer, "event: error\ndata: {\"error\": \"EMBEDDING_FAILED\"}\n\n")
		flusher.Flush()
		return
	}
	vecStr := utils.FloatSliceToVectorString(queryVec)

	// 4. Vector Search
	var query string
	var args []interface{}
	if req.CollectionID != "" {
		// Mode mới: Collection nhiều tài liệu
		docIDs, err := getCollectionDocumentIDsHelper(req.CollectionID)
		if err != nil || len(docIDs) == 0 {
			fmt.Fprintf(c.Writer, "event: error\ndata: {\"error\": \"COLLECTION_EMPTY\"}\n\n")
			flusher.Flush()
			return
		}
		query = `
			SELECT COALESCE(c.retrieval_content, c.content) AS context, c.chunk_index, c.page_number, d.title AS doc_title, 1 - (c.embedding <=> $1::vector) AS similarity
			FROM document_chunks c
			JOIN documents d ON c.document_id = d.id
			WHERE d.id = ANY($2::uuid[])
			  AND d.status = 'ready'
			  AND 1 - (c.embedding <=> $1::vector) > 0.4
			ORDER BY similarity DESC LIMIT 8`
		args = []interface{}{vecStr, docIDs}
	} else {
		// Mode cũ: 1 tài liệu
		query = `
			SELECT COALESCE(c.retrieval_content, c.content) AS context, c.chunk_index, c.page_number, d.title AS doc_title, 1 - (c.embedding <=> $1::vector) AS similarity
			FROM document_chunks c
			JOIN documents d ON c.document_id = d.id
			WHERE d.id = $2
			  AND 1 - (c.embedding <=> $1::vector) > 0.4
			ORDER BY similarity DESC LIMIT 5`
		args = []interface{}{vecStr, req.DocumentID}
	}

	rows, err := config.DB.Query(config.Ctx, query, args...)
	if err != nil {
		fmt.Fprintf(c.Writer, "event: error\ndata: {\"error\": \"DB_SEARCH_FAILED\"}\n\n")
		flusher.Flush()
		return
	}
	defer rows.Close()

	var contextText string
	var sources []map[string]interface{}
	for rows.Next() {
		var context, docTitle string
		var chunkIdx, pageNum int
		var sim float32
		rows.Scan(&context, &chunkIdx, &pageNum, &docTitle, &sim)
		
		if req.CollectionID != "" {
			contextText += fmt.Sprintf("📄 Tài liệu: %s\nĐoạn %d (Trang %d, Tương đồng: %.2f): %s\n\n", docTitle, chunkIdx, pageNum, sim, context)
		} else {
			contextText += fmt.Sprintf("Đoạn %d (Trang %d, Tương đồng: %.2f): %s\n\n", chunkIdx, pageNum, sim, context)
		}
		
		sources = append(sources, map[string]interface{}{
			"chunk_index": chunkIdx,
			"page":        pageNum,
			"similarity":  sim,
			"content":     context,
			"doc_title":   docTitle,
		})
	}

	// 5. Build prompt & Call Groq
	log.Printf("🤖 RAG: Context Length: %d chars", len(contextText))
	
	// Fallback logic (SYS-013): Nếu không có context, dùng prompt chuyên biệt để trả lời lịch sự
	if contextText == "" {
		systemPrompt = personaCfg.PromptNoContext
		if systemPrompt == "" {
			systemPrompt = "Xin lỗi, tôi không tìm thấy dữ liệu trong tài liệu gốc. Có vẻ như tài liệu đã bị xóa hoặc hết hạn."
		}
		log.Printf("⚠️ [CHAT] No context found. Using SYS-013 fallback prompt.")
	}

	finalPrompt := buildRAGPrompt(systemPrompt, historySummary, contextText, req.Question)
	log.Printf("🤖 Prompt Length: %d chars", len(finalPrompt))
	messages := []utils.ChatMessage{
		{Role: "system", Content: finalPrompt},
		{Role: "user", Content: req.Question},
	}

	// 6. Gửi sang AI Orchestrator (Tự động quản lý Groq -> Cerebras -> Mistral -> OpenRouter)
	log.Printf("🚀 [CHAT] Đang gửi yêu cầu tới AI Orchestrator cho session: %s", req.SessionID)
	chatStart := time.Now()
	
	fullAnswer, usedProvider, streamErr := utils.AI.ChatStream(utils.ServiceChat, c, messages)
	
	chatLatency := int(time.Since(chatStart).Milliseconds())

	// Log token usage (approximate)
	go func() {
		status := "ok"
		if streamErr != nil {
			status = "error"
		}
		utils.LogTokenUsage(utils.TokenUsageLog{
			UserID:      &userID,
			DocumentID:  func() *string { s := req.DocumentID; if s == "" { return nil }; return &s }(),
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

	// 7. Kết thúc file event done
	donePayload := gin.H{
		"session_id": req.SessionID,
		"message_id": uuid.New().String(),
		"sources":    sources,
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
				newQA := QAHistory{Question: req.Question, Answer: fullAnswer}
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
					"sources":    sources,
					"timestamp": time.Now().Format(time.RFC3339),
				}
				asstMsgBytes, _ := json.Marshal(assistantMsg)
				_, err := config.DB.Exec(context.Background(), `
					UPDATE chat_histories 
					SET full_messages = full_messages || $1::jsonb, 
						message_count = message_count + 1 
					WHERE session_id = $2`, string(asstMsgBytes), req.SessionID)
				
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
