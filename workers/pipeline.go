package workers

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"mindex-backend/config"
	"mindex-backend/models"
	"mindex-backend/utils"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxConcurrentEmbeds = 3

var embedSemaphore = make(chan struct{}, maxConcurrentEmbeds)

type PipelineError struct {
	Code      string
	Message   string
	Retryable bool
	Rejected  bool
	Err       error
}

func (e *PipelineError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *PipelineError) Unwrap() error {
	return e.Err
}

func RunEmbeddingPipeline(job models.UploadJob) error {
	utils.UpdateDocProgressDetail(job.DocID, "downloading", 10, "Đang đọc file gốc", "")

	localPath, cleanup, err := ensurePipelineFile(job)
	if cleanup {
		defer os.Remove(localPath)
	} else if localPath != "" {
		defer os.Remove(localPath)
	}
	if err != nil {
		utils.UpdateDocProgressDetail(job.DocID, "error", 0, "Không thể đọc file gốc", "SOURCE_FILE_UNAVAILABLE")
		return &PipelineError{Code: "SOURCE_FILE_UNAVAILABLE", Message: "Không thể đọc file gốc", Retryable: true, Err: err}
	}

	fileBytes, err := os.ReadFile(localPath)
	if err != nil {
		utils.UpdateDocProgressDetail(job.DocID, "error", 0, "Không thể đọc file tạm", "SOURCE_FILE_READ_FAILED")
		return &PipelineError{Code: "SOURCE_FILE_READ_FAILED", Message: "Không thể đọc file tạm", Retryable: true, Err: err}
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(fileBytes))
	log.Printf("[Upload Pipeline] Read file for doc %s: %d bytes", job.DocID, len(fileBytes))

	utils.UpdateDocProgressDetail(job.DocID, "extracting", 30, "Đang trích xuất nội dung", "")
	chunks, err := utils.ExtractAndChunk(localPath, utils.CleanTextLocal)
	if err != nil {
		utils.UpdateDocProgressDetail(job.DocID, "error", 0, "Không thể trích xuất nội dung", "EXTRACTION_FAILED")
		return &PipelineError{Code: "EXTRACTION_FAILED", Message: "Không thể trích xuất nội dung", Retryable: false, Err: err}
	}

	if len(job.ImagePaths) > 0 {
		utils.UpdateDocProgressDetail(job.DocID, "extracting", 33, "Đang OCR ảnh đính kèm", "")
		for i, imgPath := range job.ImagePaths {
			result, ocrErr := utils.RunImageOCR(imgPath)
			_ = os.Remove(imgPath)
			if ocrErr != nil {
				log.Printf("[Upload Pipeline] OCR failed for image %d of doc %s: %v", i+1, job.DocID, ocrErr)
				continue
			}
			if strings.TrimSpace(result.Text) == "" {
				continue
			}
			chunks = append(chunks, utils.Chunk{
				Content:          result.Text,
				RetrievalContent: fmt.Sprintf("[Nội dung từ ảnh đính kèm %d]\n\n%s", i+1, result.Text),
				PageStart:        0,
			})
		}
	}

	var cleanTextBuilder strings.Builder
	for _, chunk := range chunks {
		cleanTextBuilder.WriteString(chunk.Content + "\n\n")
	}
	cleanText := cleanTextBuilder.String()
	log.Printf("[Upload Pipeline] Extracted %d chunks from doc %s", len(chunks), job.DocID)

	utils.UpdateDocProgressDetail(job.DocID, "analyzing", 35, "Đang phân tích tổng quan", "")
	docIntel, intelErr := utils.AnalyzeDocument(job.DocID, cleanText)
	if intelErr != nil {
		log.Printf("[Upload Pipeline] Document intelligence warning for %s: %v", job.DocID, intelErr)
	}

	utils.UpdateDocProgressDetail(job.DocID, "moderating", 40, "Đang kiểm duyệt nội dung", "")
	if passed, reason := utils.T1RuleBased(config.Ctx, hash, len(strings.Fields(cleanText)), len(cleanText), cleanText); !passed {
		utils.SaveRejectedHash(config.Ctx, hash, reason)
		return &PipelineError{Code: "MODERATION_T1_REJECTED", Message: reason, Retryable: false, Rejected: true}
	}

	if passed, reason := utils.T2KeywordCheck(cleanText); !passed {
		utils.SaveRejectedHash(config.Ctx, hash, reason)
		return &PipelineError{Code: "MODERATION_T2_REJECTED", Message: reason, Retryable: false, Rejected: true}
	}

	passed, reason, subjectArea, moderationErr := utils.T3AICheck(cleanText)
	if moderationErr != nil {
		return &PipelineError{Code: "MODERATION_AI_UNAVAILABLE", Message: reason, Retryable: true, Err: moderationErr}
	}
	if !passed {
		utils.SaveRejectedHash(config.Ctx, hash, reason)
		return &PipelineError{Code: "MODERATION_AI_REJECTED", Message: reason, Retryable: false, Rejected: true}
	}
	log.Printf("[Upload Pipeline] Moderation passed. Subject: %s", subjectArea)

	utils.UpdateDocProgressDetail(job.DocID, "classifying", 45, "Đang phân loại tài liệu", "")
	updateDetectedPersona(job.DocID, cleanText)

	utils.UpdateDocProgressDetail(job.DocID, "embedding", 60, "Đang tạo embedding", "")
	if err := embedChunks(job, chunks, docIntel); err != nil {
		return err
	}

	utils.UpdateDocProgressDetail(job.DocID, "ready", 100, "Tài liệu đã sẵn sàng", "")
	_, _ = config.DB.Exec(config.Ctx, `
		UPDATE documents
		SET status='ready',
			file_hash=$1,
			processing_error_code=NULL,
			processing_error_message=NULL
		WHERE id=$2`,
		hash, job.DocID)

	utils.ClearCommunityCache()
	_, _ = config.DB.Exec(config.Ctx, `
		INSERT INTO document_references (user_id, document_id, is_owner, pinned)
		VALUES ($1, $2, TRUE, FALSE)
		ON CONFLICT DO NOTHING`,
		job.UserID, job.DocID)

	return nil
}

func ensurePipelineFile(job models.UploadJob) (string, bool, error) {
	if job.LocalPath != "" {
		if _, err := os.Stat(job.LocalPath); err == nil {
			return job.LocalPath, false, nil
		}
	}

	if job.CloudinaryURL == "" {
		return "", false, errors.New("missing cloudinary url")
	}

	uploadDir := "./tmp/uploads"
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return "", false, err
	}

	ext := filepath.Ext(job.CloudinaryURL)
	if ext == "" {
		ext = ".bin"
	}
	localPath := filepath.Join(uploadDir, fmt.Sprintf("%s-retry%s", job.DocID, ext))
	if err := downloadToFile(job.CloudinaryURL, localPath); err != nil {
		_ = os.Remove(localPath)
		return "", false, err
	}
	return localPath, true, nil
}

func downloadToFile(rawURL, dest string) error {
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func updateDetectedPersona(docID, cleanText string) {
	classifySystemPrompt := `Ban la chuyen gia phan loai tai lieu. Tra ve mot tu hoac cum tu ngan gon duy nhat cho persona phu hop.`

	classifyMessages := []utils.ChatMessage{
		{Role: "system", Content: classifySystemPrompt},
		{Role: "user", Content: fmt.Sprintf("Hay phan loai tai lieu sau thanh mot persona duy nhat:\n\n%s", firstRunes(cleanText, 2000))},
	}

	detectedPersona, usedProvider, err := utils.AI.ChatNonStream(utils.ServiceClassify, classifyMessages)
	if err != nil {
		log.Printf("[Upload Pipeline] AI classification failed for doc %s: %v", docID, err)
		return
	}
	if detectedPersona != "" {
		log.Printf("[Upload Pipeline] AI detected persona via %s: %s for doc %s", usedProvider, detectedPersona, docID)
		_, _ = config.DB.Exec(config.Ctx, `UPDATE documents SET creator_persona=$1 WHERE id=$2`, detectedPersona, docID)
	}
}

func firstRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func embedChunks(job models.UploadJob, chunks []utils.Chunk, docIntel *utils.DocIntelligence) error {
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
			finalContent := c.Content
			if docIntel != nil {
				enriched, encErr := utils.EnrichChunk(c.Content, docIntel.MainTopic)
				if encErr == nil {
					finalContent = enriched
				} else {
					log.Printf("[Upload Pipeline] Enrich error doc %s chunk %d: %v", job.DocID, idx, encErr)
				}
			}

			vec, err := utils.GeminiEmbedPool.EmbedWithRetry(finalContent, utils.CallGeminiAPI)
			latency := int(time.Since(start).Milliseconds())
			if err != nil {
				atomic.AddInt32(&errCount, 1)
				atomic.AddInt32(&completedChunks, 1)
				log.Printf("[Upload Pipeline] Embed error doc %s chunk %d: %v", job.DocID, idx, err)
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
			utils.UpdateDocProgressDetail(job.DocID, "embedding", p, "Đang tạo embedding", "")

			vecStr := utils.FloatSliceToVectorString(vec)
			_, err = config.DB.Exec(config.Ctx, `
				INSERT INTO document_chunks (document_id, chunk_index, content, retrieval_content, embedding, token_count, page_number)
				VALUES ($1, $2, $3, $4, $5::vector, $6, $7)`,
				job.DocID, idx, finalContent, c.RetrievalContent, vecStr, len(finalContent)/4, c.PageStart,
			)
			if err != nil {
				atomic.AddInt32(&errCount, 1)
				log.Printf("[Upload Pipeline] Chunk DB error doc %s chunk %d: %v", job.DocID, idx, err)
			}
		}(i, chunkObj)
	}
	wg.Wait()

	if errCount > 0 {
		_, _ = config.DB.Exec(config.Ctx, `DELETE FROM document_chunks WHERE document_id=$1`, job.DocID)
		utils.UpdateDocProgressDetail(job.DocID, "error", 0, "Tạo embedding thất bại", "EMBEDDING_FAILED")
		return &PipelineError{
			Code:      "EMBEDDING_FAILED",
			Message:   fmt.Sprintf("Embedding pipeline failed with %d chunk errors", errCount),
			Retryable: true,
		}
	}

	return nil
}
