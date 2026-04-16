package workers

import (
	"crypto/sha256"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/controllers"
	"mindex-backend/utils"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxConcurrentEmbeds = 3

var embedSemaphore = make(chan struct{}, maxConcurrentEmbeds)

func RunEmbeddingPipeline(job controllers.UploadJob) error {
	// 1. Đọc file từ Local Path (nơi Controller đã lưu tạm)
	utils.UpdateDocProgress(job.DocID, "downloading", 10)
	
	// Tự động dọn dẹp file tạm khi pipeline hoàn tất (thành công hoặc lỗi)
	defer os.Remove(job.LocalPath)

	fileBytes, err := os.ReadFile(job.LocalPath)
	if err != nil {
		utils.UpdateDocProgress(job.DocID, "error", 0)
		return fmt.Errorf("không thể đọc file từ hệ thống: %v", err)
	}
	log.Printf("📥 Read file from local path for Doc %s: %d bytes", job.DocID, len(fileBytes))

	// 2. Tính hash để đối chiếu (Controller đã lưu hash này, nhưng Worker vẫn dùng để log/audit)
	hash := fmt.Sprintf("%x", sha256.Sum256(fileBytes))


	// 3. Khởi tạo Extraction & Chunking (Structure-aware)
	utils.UpdateDocProgress(job.DocID, "extracting", 30)
	chunks, err := utils.ExtractAndChunk(job.LocalPath, utils.CleanTextLocal)
	if err != nil {
		utils.UpdateDocProgress(job.DocID, "error", 0)
		return fmt.Errorf("lỗi trích xuất và chunking: %v", err)
	}
	
	// Tái tạo cleanText để dùng cho Moderation và Classification
	var cleanTextBuilder strings.Builder
	for _, chunk := range chunks {
		cleanTextBuilder.WriteString(chunk.Content + "\n\n")
	}
	cleanText := cleanTextBuilder.String()
	log.Printf("📄 Extracted %d chunks from Doc %s", len(chunks), job.DocID)

	// 3a. Document Intelligence (Nghiên cứu tài liệu toàn diện)
	utils.UpdateDocProgress(job.DocID, "analyzing", 35)
	docIntel, intelErr := utils.AnalyzeDocument(job.DocID, cleanText)
	if intelErr != nil {
		log.Printf("⚠️ [Pipeline Warning] Doc Intelligence failed for %s: %v. Continuing without big picture metadata.", job.DocID, intelErr)
	}

	// Kiểm duyệt
	utils.UpdateDocProgress(job.DocID, "moderating", 40)
	if passed, reason := utils.T1RuleBased(config.Ctx, hash, len(strings.Fields(cleanText)), len(cleanText), cleanText); !passed {
		config.DB.Exec(config.Ctx, `UPDATE documents SET status='error' WHERE id=$1`, job.DocID)
		return fmt.Errorf("kiểm duyệt Tầng 1 thất bại: %s", reason)
	}

	passed, reason, subjectArea := utils.T3AICheck(cleanText)
	if !passed {
		config.DB.Exec(config.Ctx, `UPDATE documents SET status='error' WHERE id=$1`, job.DocID)
		return fmt.Errorf("kiểm duyệt AI thất bại: %s", reason)
	}
	log.Printf("✅ Moderation Passed. Subject: %s", subjectArea)

	// 3b. AI Classification (Lĩnh vực) - dùng AI Orchestrator (Ưu tiên Gemini -> Cerebras -> Groq -> HF)
	utils.UpdateDocProgress(job.DocID, "classifying", 45)
	
	classifySystemPrompt := `Bạn là chuyên gia phân loại tài liệu. Nhiệm vụ của bạn là xác định đối tượng người dùng (Persona) phù hợp nhất với tài liệu này.
Chỉ trả về MỘT từ hoặc cụm từ ngắn gọn duy nhất (ví dụ: Sinh viên, Lập trình viên, Luật sư, Bác sĩ, Giảng viên, Học sinh, Kỹ sư, v.v.).
TUYỆT ĐỐI KHÔNG tóm tắt nội dung, không giải thích thêm.`

	classifyMessages := []utils.ChatMessage{
		{Role: "system", Content: classifySystemPrompt},
		{Role: "user", Content: fmt.Sprintf("Hãy phân loại tài liệu sau đây thành một Persona duy nhất:\n\n%s", func() string {
			runes := []rune(cleanText)
			if len(runes) > 2000 {
				return string(runes[:2000])
			}
			return cleanText
		}())},
	}
	
	detectedPersona, usedProvider, err := utils.AI.ChatNonStream(utils.ServiceClassify, classifyMessages)
	if err != nil {
		log.Printf("⚠️ [Pipeline Error] AI Classification failed for Doc %s: %v. Keeping default persona.", job.DocID, err)
	} else if detectedPersona != "" {
		log.Printf("🏷️ [Pipeline Success] AI detected persona (via %s): %s for Doc %s", usedProvider, detectedPersona, job.DocID)
		_, _ = config.DB.Exec(config.Ctx, `UPDATE documents SET creator_persona=$1 WHERE id=$2`, detectedPersona, job.DocID)
	}

	// Embedding
	utils.UpdateDocProgress(job.DocID, "embedding", 60)
	// utils.ExtractAndChunk đã chia chunks ở bước trên
	
	log.Printf("🧩 Doc %s: Using %d structured chunks. Starting embedding...", job.DocID, len(chunks))

	var wg sync.WaitGroup
	var errCount int32
	var completedChunks int32

	for i, chunkObj := range chunks {
		wg.Add(1)
		go func(idx int, c utils.Chunk) {
			defer wg.Done()
			embedSemaphore <- struct{}{}
			defer func() { <-embedSemaphore }()

			start := time.Now()

			// --- CẢI TIẾN: Contextual Enrichment ---
			// Chỉ làm giàu nếu có Doc Intelligence (cần tóm tắt/chủ đề chính)
			finalContent := c.Content
			if docIntel != nil {
				enriched, encErr := utils.EnrichChunk(c.Content, docIntel.MainTopic)
				if encErr == nil {
					finalContent = enriched
				} else {
					log.Printf("⚠️ [Enrich Error] Chunk %d in %s: %v", idx, job.DocID, encErr)
				}
			}

			// GeminiEmbedPool nhận nội dung đã được làm giàu (Enriched)
			vec, err := utils.GeminiEmbedPool.EmbedWithRetry(finalContent, utils.CallGeminiAPI)
			latency := int(time.Since(start).Milliseconds())
			
			if err != nil {
				atomic.AddInt32(&errCount, 1)
				atomic.AddInt32(&completedChunks, 1)
				log.Printf("❌ [Embed Error] Doc %s Chunk %d: %v", job.DocID, idx, err)
				
				utils.LogTokenUsage(utils.TokenUsageLog{
					UserID:      &job.UserID,
					DocumentID:  &job.DocID,
					Service:     "gemini_embed",
					Operation:   "upload",
					TotalTokens: len(c.Content) / 4,
					LatencyMs:   latency,
					Status:      "error",
					ErrorCode:   err.Error(),
				})
				return
			}

			// Log Success
			utils.LogTokenUsage(utils.TokenUsageLog{
				UserID:      &job.UserID,
				DocumentID:  &job.DocID,
				Service:     "gemini_embed",
				Operation:   "upload",
				TotalTokens: len(c.Content) / 4,
				LatencyMs:   latency,
				Status:      "ok",
			})

			atomic.AddInt32(&completedChunks, 1)
			p := 60 + int(float32(atomic.LoadInt32(&completedChunks))/float32(len(chunks))*35)
			utils.UpdateDocProgress(job.DocID, "embedding", p)

			vecStr := utils.FloatSliceToVectorString(vec)
			_, err = config.DB.Exec(config.Ctx, `
				INSERT INTO document_chunks (document_id, chunk_index, content, retrieval_content, embedding, token_count, page_number)
				VALUES ($1, $2, $3, $4, $5::vector, $6, $7)`,
				job.DocID, idx, finalContent, c.RetrievalContent, vecStr, len(finalContent)/4, c.PageStart,
			)
			if err != nil {
				atomic.AddInt32(&errCount, 1)
				log.Printf("❌ [DB Error] Doc %s Chunk %d: %v", job.DocID, idx, err)
			}
		}(i, chunkObj)
	}
	wg.Wait()

	if errCount > 0 {
		log.Printf("⚠️ [Pipeline Warning] Doc %s finished with %d errors.", job.DocID, errCount)
	}

	utils.UpdateDocProgress(job.DocID, "ready", 100)
	_, _ = config.DB.Exec(config.Ctx, `
		UPDATE documents SET status='ready', file_hash=$1, cloudinary_url=NULL WHERE id=$2`,
		hash, job.DocID)

	// Làm mới cache cộng đồng (đảm bảo nếu doc đã được share thì sẽ hiện lên search ngay)
	utils.ClearCommunityCache()

	_, _ = config.DB.Exec(config.Ctx, `
		INSERT INTO document_references (user_id, document_id, is_owner, pinned)
		VALUES ($1, $2, TRUE, FALSE) ON CONFLICT DO NOTHING`,
		job.UserID, job.DocID)

	return nil
}

func deleteFromCloudinary(url string) error {
	return nil
}


