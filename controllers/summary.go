package controllers

import (
	"encoding/json"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/internal/persona"
	"mindex-backend/utils"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type SummaryRequest struct {
	DocumentID string `json:"document_id" binding:"required"`
	Mode       string `json:"mode"` // quick, academic, deep
}

func GetCacheKey(mode, docID, persona string) string {
	return fmt.Sprintf("summary:%s:%s:%s", mode, docID, persona)
}

// Semaphore để giới hạn số lượng request đồng thời tới Groq (Rate Limit Protection)
var groqSemaphore = make(chan struct{}, 3)

func QuickSummary(c *gin.Context) {
	var req SummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Dữ liệu không hợp lệ"})
		return
	}
	userPersona := c.GetString("persona")
	if userPersona == "" {
		userPersona = "student"
	}
	prompt := persona.Cache.Get(userPersona).PromptSummaryQuick

	// Survival of the Fittest: Gia hạn nếu là tài liệu cộng đồng
	go RefreshPublicDocExpiry(req.DocumentID)

	cacheKey := GetCacheKey("quick", req.DocumentID, userPersona)
	// 1. Kiểm tra cache Redis
	if config.RedisClient != nil {
		val, err := config.RedisClient.Get(config.Ctx, cacheKey).Result()
		if err == nil {
			log.Printf("🔍 [QuickSummary] Cache hit for doc: %s", req.DocumentID)
			var s map[string]interface{}
			if err := json.Unmarshal([]byte(val), &s); err == nil {
				// Vẫn cần chuẩn hóa tên trường từ cache cũ
				normalizeFields(s)
				c.JSON(200, gin.H{"success": true, "data": gin.H{"summary": s, "from_cache": true}})
				return
			}
		} else if err.Error() != "redis: nil" {
			log.Printf("⚠️ [QuickSummary] Redis Error (Skipping Cache): %v", err)
		}
	}

	chunks := getAllDocumentChunks(req.DocumentID)
	var finalSummary string
	content := strings.Join(chunks, "\n\n")

	systemPrompt := `Bạn là chuyên gia phân tích tài liệu học thuật.`
	// Apply Mindex Branding (Identity and Tone, excluding Opening Greeting for JSON)
	systemPrompt = utils.ApplyMindexBrandingSummary(systemPrompt)

	systemPrompt += `
Bạn CHỈ trả về duy nhất định dạng JSON thuần, không bao gồm markdown hay giải thích. 
Cấu trúc JSON bắt buộc: { 
  "overview": "tóm tắt tổng quan nội dung bằng tiếng Việt (dùng markdown nếu cần)", 
  "key_points": ["danh sách các ý chính bằng tiếng Việt"], 
  "concepts": [{"t": "thuật ngữ", "d": "định nghĩa ngắn"}], 
  "application": "khả năng ứng dụng thực tế" 
}`

	// --- 2. Thử tóm tắt bằng AI Orchestrator (Ưu tiên Gemini 2.5 Flash -> Mistral -> Groq -> OpenRouter) ---
	log.Printf("🤖 [QuickSummary] Đang sử dụng AI Orchestrator cho tài liệu %s...", req.DocumentID)
	messages := []utils.ChatMessage{
		{Role: "system", Content: systemPrompt + "\n" + prompt},
		{Role: "user", Content: fmt.Sprintf("Hãy tóm tắt nội dung tài liệu học tập sau đây:\n\n%s", content)},
	}
	
	summaryStart := time.Now()
	res, usedProvider, err := utils.AI.ChatNonStream(utils.ServiceSummary, messages)
	summaryLatency := int(time.Since(summaryStart).Milliseconds())

	if err == nil && res != "" {
		log.Printf("✅ [QuickSummary] AI Orchestrator đã hoàn thành tóm tắt.")
		finalSummary = utils.CleanJSONString(res)
		// Log success
		utils.LogTokenUsage(utils.TokenUsageLog{
			DocumentID: &req.DocumentID,
			Service:    string(usedProvider),
			Operation:  "summary_quick",
			TotalTokens: len(content) / 4,
			LatencyMs:  summaryLatency,
			KeyAlias:   "auto_fallback",
			Status:     "ok",
		})
	} else {
		log.Printf("❌ [QuickSummary] AI Orchestrator thất bại: %v", err)
		c.JSON(500, gin.H{"error": "AI_SERVICE_DOWN"})
		return
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(finalSummary), &parsed); err != nil {
		log.Printf("❌ [QuickSummary] JSON Unmarshal Error: %v | Raw: %s", err, finalSummary)
		// Fallback nếu AI không trả về đúng JSON nhưng có nội dung text
		parsed = map[string]interface{}{
			"overview":    finalSummary,
			"key_points":  []string{"Xem chi tiết ở mục tổng quan"},
			"concepts":    []interface{}{},
			"application": "Vui lòng xem nội dung tóm tắt phía trên.",
		}
	} else {
		normalizeFields(parsed)
	}

	// Cache
	cacheBytes, _ := json.Marshal(parsed)
	if config.RedisClient != nil {
		config.RedisClient.Set(config.Ctx, cacheKey, cacheBytes, 24*time.Hour)
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"document_id": req.DocumentID,
			"from_cache":  false,
			"summary":     parsed,
		},
	})
}

func DetailedSummary(c *gin.Context) {
	var req SummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Dữ liệu không hợp lệ"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	flusher, _ := c.Writer.(http.Flusher)

	userPersona := c.GetString("persona")
	if userPersona == "" {
		userPersona = "student"
	}
	
	mode := req.Mode
	if mode == "" {
		mode = "academic"
	}

	// Survival of the Fittest: Gia hạn nếu là tài liệu cộng đồng
	go RefreshPublicDocExpiry(req.DocumentID)

	cacheKey := GetCacheKey(mode, req.DocumentID, userPersona)

	// 1. Kiểm tra cache Redis trước khi stream
	if config.RedisClient != nil {
		val, err := config.RedisClient.Get(config.Ctx, cacheKey).Result()
		if err == nil && val != "" {
			log.Printf("🔍 [DetailedSummary] Cache HIT for mode %s", mode)
			// Giả lập stream cực nhanh từ cache để giữ UX đồng nhất
			fmt.Fprintf(c.Writer, "event: info\ndata: {\"message\": \"Đã tìm thấy tóm tắt trong bộ nhớ đệm. Đang tải...\"}\n\n")
			flusher.Flush()
			
			// Gửi toàn bộ nội dung trong 1 token lớn
			tokenPayload, _ := json.Marshal(map[string]string{"token": val})
			fmt.Fprintf(c.Writer, "event: token\ndata: %s\n\n", string(tokenPayload))
			fmt.Fprintf(c.Writer, "event: done\ndata: {\"status\": \"completed\", \"from_cache\": true}\n\n")
			flusher.Flush()
			return
		}
	}

	prompt := ""
	if mode == "deep" {
		prompt = persona.Cache.Get(userPersona).PromptSummaryDetailed // Có thể tùy chỉnh prompt riêng cho deep insight sau
	} else {
		prompt = persona.Cache.Get(userPersona).PromptSummaryDetailed
	}
	// Apply Mindex Branding (Identity, Tone, Opening) - Tóm tắt luôn được coi là tin nhắn đầu
	prompt = utils.ApplyMindexBranding(prompt, true)

	chunks := getAllDocumentChunks(req.DocumentID)
	contextText := strings.Join(chunks, "\n\n")

	// --- Sử dụng AI Orchestrator cho Detailed Summary ---
	log.Printf("🤖 [DetailedSummary] Đang sử dụng AI Orchestrator cho mode %s...", mode)
	
	messages := []utils.ChatMessage{
		{Role: "system", Content: prompt},
		{Role: "user", Content: fmt.Sprintf("Vui lòng thực hiện tóm tắt chi tiết (mode: %s) cho nội dung tài liệu dưới đây:\n\n%s", mode, contextText)},
	}

	summaryStart := time.Now()
	finalAnswer, usedProvider, err := utils.AI.ChatStream(utils.ServiceSummary, c, messages)
	summaryLatency := int(time.Since(summaryStart).Milliseconds())

	if err == nil && finalAnswer != "" {
		log.Printf("✅ [DetailedSummary] AI Orchestrator hoàn thành nhiệm vụ.")
		
		// Log Token Usage
		utils.LogTokenUsage(utils.TokenUsageLog{
			DocumentID:  &req.DocumentID,
			Service:     string(usedProvider),
			Operation:   "summary_detailed",
			TotalTokens: (len(contextText) + len(finalAnswer)) / 4,
			LatencyMs:   summaryLatency,
			KeyAlias:    "auto_fallback",
			Status:      "ok",
		})

		if config.RedisClient != nil {
			config.RedisClient.Set(config.Ctx, cacheKey, finalAnswer, 24*time.Hour)
		}
		fmt.Fprintf(c.Writer, "event: done\ndata: {\"status\": \"completed\"}\n\n")
		flusher.Flush()
		return
	} else {
		log.Printf("❌ [DetailedSummary] AI Orchestrator thất bại: %v", err)
	}

	fmt.Fprintf(c.Writer, "event: done\ndata: {\"status\": \"completed\"}\n\n")
	flusher.Flush()
}

func GetCachedSummary(c *gin.Context) {
	docID := c.Param("id")
	mode := c.Query("mode")
	userPersona := c.GetString("persona")
	if userPersona == "" {
		userPersona = "student"
	}

	if docID == "" || mode == "" {
		c.JSON(400, gin.H{"error": "Thiếu thông tin docID hoặc mode"})
		return
	}

	cacheKey := GetCacheKey(mode, docID, userPersona)
	if config.RedisClient == nil {
		c.JSON(200, gin.H{"success": false, "message": "Redis không sẵn sàng"})
		return
	}

	val, err := config.RedisClient.Get(config.Ctx, cacheKey).Result()
	if err != nil {
		c.JSON(200, gin.H{"success": false, "message": "Không tìm thấy dữ liệu trong cache"})
		return
	}

	// Nếu là quick scan, cần parse JSON
	if mode == "quick" {
		var s map[string]interface{}
		if err := json.Unmarshal([]byte(val), &s); err == nil {
			normalizeFields(s)
			c.JSON(200, gin.H{"success": true, "data": gin.H{"summary": s}})
			return
		}
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"summary": val}})
}

// runParallelMap thực hiện tóm tắt các khối chunks một cách song song
func runParallelMap(chunks []string, batchSize int) []string {
	var miniSummaries []string
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := 0; i < len(chunks); i += batchSize {
		end := i + batchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		
		batchContent := strings.Join(chunks[i:end], "\n\n")
		wg.Add(1)
		
		go func(content string, index int) {
			defer wg.Done()
			
			// Acquire semaphore
			log.Printf("🤖 worker %d: đang chờ xử lý phần (batch)...", index/batchSize)
			groqSemaphore <- struct{}{}
			defer func() { <-groqSemaphore }()
			
			log.Printf("🚀 worker %d: Đang gửi yêu cầu tới Groq cho %d words...", index/batchSize, len(strings.Fields(content)))
			miniPrompt := fmt.Sprintf("Hãy tóm tắt ngắn gọn các luận điểm quan trọng nhất (theo dạng bullet points) trong phần văn bản sau của tài liệu học tập:\n%s", content)
			
			// Retry logic nâng cao: Exponential Backoff cho Map phase
			var res string
			var err error
			for retry := 0; retry < 3; retry++ {
				res, err = utils.StreamGroqChatNonStream([]utils.ChatMessage{{Role: "user", Content: miniPrompt}})
				if err == nil {
					break
				}
				
				waitSec := (retry + 1) * 4 // 4s, 8s, 12s
				log.Printf("⚠️ worker %d (thử lại %d): Lỗi API Groq (%v). Thử lại sau %ds...", index/batchSize, retry+1, err, waitSec)
				time.Sleep(time.Duration(waitSec) * time.Second)
			}
			
			if err == nil && res != "" {
				mu.Lock()
				miniSummaries = append(miniSummaries, res)
				mu.Unlock()
				log.Printf("✨ worker %d: Đã hoàn thành tóm tắt phần này.", index/batchSize)
			} else {
				log.Printf("❌ worker %d: Thất bại sau các lần thử lại.", index/batchSize)
			}
		}(batchContent, i)
	}
	
	wg.Wait()
	return miniSummaries
}

func getAllDocumentText(docID string, limit int) string {
	q := `SELECT COALESCE(retrieval_content, content) FROM document_chunks WHERE document_id = $1 ORDER BY chunk_index ASC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, _ := config.DB.Query(config.Ctx, q, docID)
	defer rows.Close()

	var text string
	for rows.Next() {
		var c string
		rows.Scan(&c)
		text += c + "\n\n"
	}
	return text
}

func getAllDocumentChunks(docID string) []string {
	rows, _ := config.DB.Query(config.Ctx, `SELECT COALESCE(retrieval_content, content) FROM document_chunks WHERE document_id = $1 ORDER BY chunk_index ASC`, docID)
	defer rows.Close()

	var chunks []string
	for rows.Next() {
		var c string
		rows.Scan(&c)
		chunks = append(chunks, c)
	}
	return chunks
}

func normalizeFields(parsed map[string]interface{}) {
	normalize := func(keys []string, target string) {
		if _, ok := parsed[target]; !ok {
			for _, k := range keys {
				if v, ok := parsed[k]; ok {
					parsed[target] = v
					return
				}
			}
		}
	}
	normalize([]string{"tong_quan", "tổng_quan", "nội_dung", "tóm_tắt", "noi_dung_tong_quan"}, "overview")
	normalize([]string{"key_points", "ý_chính", "y_chinh", "points", "điểm_chính"}, "key_points")
	normalize([]string{"concepts", "khái_niệm", "thuat_ngu", "thuật_ngữ", "core_concepts"}, "concepts")
	normalize([]string{"application", "ứng_dụng", "ung_dung", "thực_tế"}, "application")
}
