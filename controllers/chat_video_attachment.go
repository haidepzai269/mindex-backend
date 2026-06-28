package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	maxChatVideoUploadBytes      int64 = 50 << 20
	maxVideoDurationSeconds            = 600
	maxVideoContextChars               = 20000
	videoProcessingTimeout             = 5 * time.Minute
	videoResultKeyPrefix               = "mindex:processing:result:"
	videoProcessingQueueName           = "mindex:processing:requests"
)

func getVideoUploadQuota(userID, role string) (limit, used int, resetAt time.Time, err error) {
	tier := getUserTierForChat(userID, role)
	switch tier {
	case "PRO":
		limit = 4
	case "ULTRA":
		limit = 20
	case "ADMIN":
		limit = 9999
	default:
		limit = 2
	}

	var count int
	var oldest *time.Time
	err = config.DB.QueryRow(config.Ctx, `
		SELECT COUNT(*), MIN(created_at)
		FROM chat_video_attachments
		WHERE user_id = $1 AND created_at > NOW() - INTERVAL '24 hours'
		  AND status != 'error'`,
		userID).Scan(&count, &oldest)
	if err != nil {
		return limit, limit, time.Now().Add(24 * time.Hour), err
	}
	used = count
	if oldest != nil {
		resetAt = oldest.Add(24 * time.Hour)
	} else {
		resetAt = time.Now().Add(24 * time.Hour)
	}
	return limit, used, resetAt, nil
}

func GetVideoUploadQuota(c *gin.Context) {
	userID := c.GetString("user_id")
	role := c.GetString("role")
	tier := getUserTierForChat(userID, role)
	limit, used, resetAt, err := getVideoUploadQuota(userID, role)
	if err != nil {
		log.Printf("[VideoAttachment] quota query failed user=%s: %v", userID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Không thể kiểm tra quota"})
		return
	}
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	c.JSON(http.StatusOK, gin.H{
		"success":   true,
		"tier":      tier,
		"limit":     limit,
		"used":      used,
		"remaining": remaining,
		"reset_at":  resetAt,
	})
}

func UploadChatVideoAttachment(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxChatVideoUploadBytes+(2<<20))

	sessionID := strings.TrimSpace(c.PostForm("session_id"))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Thiếu session_id"})
		return
	}

	userID := c.GetString("user_id")

	var exists bool
	err := config.DB.QueryRow(config.Ctx, `
		SELECT EXISTS(SELECT 1 FROM chat_histories WHERE session_id = $1 AND user_id = $2 AND chat_scope = $3)`,
		sessionID, userID, globalAIChatScope).Scan(&exists)
	if err != nil || !exists {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Session không hợp lệ"})
		return
	}

	role := c.GetString("role")
	quotaLimit, quotaUsed, resetAt, quotaErr := getVideoUploadQuota(userID, role)
	if quotaErr != nil {
		log.Printf("[VideoAttachment] quota check failed user=%s: %v", userID, quotaErr)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Không thể kiểm tra giới hạn upload"})
		return
	}
	if quotaUsed >= quotaLimit {
		hoursLeft := time.Until(resetAt).Hours()
		c.JSON(http.StatusTooManyRequests, gin.H{
			"success": false,
			"message": "Ban da dat gioi han upload video. Nang goi de su dung them.",
			"quota": gin.H{
				"tier":        getUserTierForChat(userID, role),
				"limit":       quotaLimit,
				"used":        quotaUsed,
				"reset_hours": hoursLeft,
			},
		})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Thiếu file video"})
		return
	}
	if fileHeader.Size <= 0 || fileHeader.Size > maxChatVideoUploadBytes {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Video tối đa 50MB"})
		return
	}

	mimeType := strings.ToLower(fileHeader.Header.Get("Content-Type"))
	if !isAllowedVideoMime(mimeType) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Chỉ hỗ trợ định dạng MP4, WebM, MOV"})
		return
	}

	attachmentID := uuid.New().String()
	tmpDir := filepath.Join(os.TempDir(), "mindex-chat-videos")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Lỗi hệ thống"})
		return
	}
	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if ext == "" {
		ext = ".mp4"
	}
	tmpPath := filepath.Join(tmpDir, attachmentID+ext)
	defer os.Remove(tmpPath)

	if err := c.SaveUploadedFile(fileHeader, tmpPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Không thể lưu file video"})
		return
	}

	// T015: SHA-256 hash computation
	contentHash, hashErr := computeFileHash(tmpPath)
	if hashErr != nil {
		log.Printf("[VideoAttachment] hash computation failed user=%s: %v", userID, hashErr)
	}

	duration, probeErr := probeVideoDuration(tmpPath)
	if probeErr != nil {
		log.Printf("[VideoAttachment] ffprobe failed user=%s: %v", userID, probeErr)
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Video không hợp lệ hoặc codec không được hỗ trợ"})
		return
	}
	if duration > maxVideoDurationSeconds {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Video tối đa 10 phút"})
		return
	}

	// T016: Cache lookup
	if contentHash != "" {
		var cacheID string
		var hasAudio bool
		var audioTranscript, contextText string
		var frameCount, originalDuration int
		var frameAnalysis []byte
		cacheErr := config.DB.QueryRow(config.Ctx, `
			SELECT id, has_audio, audio_transcript, frame_count, frame_analysis, context_text, original_duration
			FROM video_analysis_cache WHERE content_hash = $1`, contentHash).Scan(
			&cacheID, &hasAudio, &audioTranscript, &frameCount, &frameAnalysis, &contextText, &originalDuration)
		if cacheErr == nil {
			// Look up storage_url from a previous upload with the same content
			var origStorageURL, origPublicID string
			_ = config.DB.QueryRow(config.Ctx, `
				SELECT storage_url, storage_public_id FROM chat_video_attachments
				WHERE content_hash = $1 AND storage_url != '' LIMIT 1`, contentHash).Scan(&origStorageURL, &origPublicID)

			// Cache hit - insert attachment as done without Cloudinary upload
			_, insertErr := config.DB.Exec(config.Ctx, `
				INSERT INTO chat_video_attachments
					(id, user_id, session_id, original_name, mime_type, size_bytes, duration_seconds,
					 storage_url, storage_public_id, status, content_hash, context_text, has_audio, audio_transcript, frame_count)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'done', $10, $11, $12, $13, $14)`,
				attachmentID, userID, sessionID, fileHeader.Filename, mimeType, fileHeader.Size, originalDuration,
				origStorageURL, origPublicID, contentHash, contextText, hasAudio, audioTranscript, frameCount)
			if insertErr != nil {
				log.Printf("[VideoAttachment] cache hit but DB insert failed user=%s: %v", userID, insertErr)
			} else {
				c.JSON(http.StatusOK, gin.H{
					"success":          true,
					"attachment_id":    attachmentID,
					"session_id":       sessionID,
					"status":           "done",
					"cached":           true,
					"has_audio":        hasAudio,
					"frame_count":      frameCount,
					"original_name":    fileHeader.Filename,
					"duration_seconds": originalDuration,
					"storage_url":      origStorageURL,
				})
				return
			}
		}
	}

	publicID := fmt.Sprintf("mindex_chat_videos/%s/%s", userID, attachmentID)
	upload, err := utils.UploadVideoToCloudinary(tmpPath, publicID)
	if err != nil {
		log.Printf("[VideoAttachment] Cloudinary upload failed user=%s: %v", userID, err)
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "Không thể upload video"})
		return
	}

	_, err = config.DB.Exec(config.Ctx, `
		INSERT INTO chat_video_attachments
			(id, user_id, session_id, original_name, mime_type, size_bytes, duration_seconds, storage_url, storage_public_id, status, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'uploaded', $10)`,
		attachmentID, userID, sessionID, fileHeader.Filename, mimeType, fileHeader.Size, duration, upload.SecureURL, upload.PublicID, contentHash)
	if err != nil {
		if delErr := utils.DestroyVideoFromCloudinary(upload.PublicID); delErr != nil {
			log.Printf("[VideoAttachment] Cloudinary cleanup failed after DB error publicID=%s: %v", upload.PublicID, delErr)
		}
		log.Printf("[VideoAttachment] DB insert failed user=%s: %v", userID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Không thể lưu thông tin video"})
		return
	}

	go startVideoProcessing(attachmentID, userID, sessionID, upload.SecureURL, duration, fileHeader.Filename)

	c.JSON(http.StatusOK, gin.H{
		"success":          true,
		"attachment_id":    attachmentID,
		"session_id":       sessionID,
		"status":           "processing",
		"cached":           false,
		"original_name":    fileHeader.Filename,
		"mime_type":        mimeType,
		"size_bytes":       fileHeader.Size,
		"duration_seconds": duration,
		"storage_url":      upload.SecureURL,
	})
}

func GetVideoAttachmentStatus(c *gin.Context) {
	attachmentID := c.Param("attachment_id")
	userID := c.GetString("user_id")

	if _, err := uuid.Parse(attachmentID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid attachment ID"})
		return
	}

	var status, errorMessage, contextText string
	var hasAudio bool
	var frameCount int
	var createdAt time.Time
	err := config.DB.QueryRow(config.Ctx, `
		SELECT status, COALESCE(error_message, ''), has_audio, frame_count, context_text, created_at
		FROM chat_video_attachments
		WHERE id = $1 AND user_id = $2`,
		attachmentID, userID).Scan(&status, &errorMessage, &hasAudio, &frameCount, &contextText, &createdAt)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Video attachment not found"})
		return
	}

	if status == "processing" {
		if time.Since(createdAt) > videoProcessingTimeout {
			markVideoError(attachmentID, "Video processing timed out")
			status = "error"
			errorMessage = "Video processing timed out"
		} else {
			checkVideoProcessingResult(attachmentID, userID)
			_ = config.DB.QueryRow(config.Ctx, `
				SELECT status, COALESCE(error_message, ''), has_audio, frame_count, context_text
				FROM chat_video_attachments
				WHERE id = $1 AND user_id = $2`,
				attachmentID, userID).Scan(&status, &errorMessage, &hasAudio, &frameCount, &contextText)
		}
	}

	resp := gin.H{
		"success":       true,
		"attachment_id": attachmentID,
		"status":        status,
	}

	switch status {
	case "done":
		resp["has_audio"] = hasAudio
		resp["frame_count"] = frameCount
		resp["context_preview"] = compactChatPreview(contextText, 200)
	case "error":
		resp["error_message"] = errorMessage
	}

	c.JSON(http.StatusOK, resp)
}

func computeFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func startVideoProcessing(attachmentID, _, _, videoURL string, duration int, originalName string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[VideoAttachment] panic in startVideoProcessing id=%s: %v", attachmentID, r)
			markVideoError(attachmentID, "Internal processing error")
		}
	}()

	_, err := config.DB.Exec(config.Ctx, `
		UPDATE chat_video_attachments SET status = 'processing' WHERE id = $1`,
		attachmentID)
	if err != nil {
		log.Printf("[VideoAttachment] Failed to update status to processing id=%s: %v", attachmentID, err)
		markVideoError(attachmentID, "Failed to start processing")
		return
	}

	taskID := uuid.New().String()

	_, err = config.DB.Exec(config.Ctx, `
		UPDATE chat_video_attachments SET message_id = $1 WHERE id = $2`,
		taskID, attachmentID)
	if err != nil {
		log.Printf("[VideoAttachment] Failed to store task_id id=%s: %v", attachmentID, err)
	}

	req := map[string]any{
		"task_id":   taskID,
		"task_type": "video",
		"payload": map[string]any{
			"video_url":        videoURL,
			"duration_seconds": duration,
			"attachment_id":    attachmentID,
			"original_name":    originalName,
		},
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(req)
	if err != nil {
		markVideoError(attachmentID, "Failed to marshal processing request")
		return
	}

	if err := config.RedisClient.RPush(config.Ctx, videoProcessingQueueName, data).Err(); err != nil {
		markVideoError(attachmentID, "Failed to enqueue video task")
		log.Printf("[VideoAttachment] Redis RPUSH failed id=%s: %v", attachmentID, err)
	}
}

func checkVideoProcessingResult(attachmentID, userID string) {
	var taskID string
	err := config.DB.QueryRow(config.Ctx, `
		SELECT COALESCE(message_id, '') FROM chat_video_attachments WHERE id = $1 AND user_id = $2`,
		attachmentID, userID).Scan(&taskID)
	if err != nil || taskID == "" {
		return
	}

	resultKey := videoResultKeyPrefix + taskID
	raw, err := config.RedisClient.GetDel(config.Ctx, resultKey).Bytes()
	if err == redis.Nil {
		return
	}
	if err != nil {
		log.Printf("[VideoAttachment] Redis GETDEL failed key=%s: %v", resultKey, err)
		return
	}

	var result struct {
		Status     string          `json:"status"`
		Result     json.RawMessage `json:"result"`
		Error      *string         `json:"error"`
		DurationMs int             `json:"duration_ms"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		markVideoError(attachmentID, "Failed to parse processing result")
		return
	}

	if result.Status == "failed" {
		errMsg := "Processing failed"
		if result.Error != nil {
			errMsg = *result.Error
		}
		markVideoError(attachmentID, errMsg)
		return
	}

	var videoResult struct {
		HasAudio        bool            `json:"has_audio"`
		AudioTranscript string          `json:"audio_transcript"`
		FrameCount      int             `json:"frame_count"`
		Frames          json.RawMessage `json:"frames"`
		ContextText     string          `json:"context_text"`
	}
	if err := json.Unmarshal(result.Result, &videoResult); err != nil {
		markVideoError(attachmentID, "Failed to parse video result data")
		return
	}

	contextText := videoResult.ContextText
	if len(contextText) > maxVideoContextChars {
		contextText = contextText[:maxVideoContextChars]
	}

	framesJSON := videoResult.Frames
	if len(framesJSON) == 0 {
		framesJSON = json.RawMessage("[]")
	}

	_, err = config.DB.Exec(config.Ctx, `
		UPDATE chat_video_attachments
		SET status = 'done', has_audio = $1, audio_transcript = $2, frame_count = $3,
		    frame_analysis = $4::jsonb, context_text = $5
		WHERE id = $6`,
		videoResult.HasAudio, videoResult.AudioTranscript, videoResult.FrameCount,
		string(framesJSON), contextText, attachmentID)
	if err != nil {
		log.Printf("[VideoAttachment] Failed to update done status id=%s: %v", attachmentID, err)
		return
	}

	// T017: Write to video_analysis_cache
	var contentHash string
	var duration int
	_ = config.DB.QueryRow(config.Ctx, `SELECT COALESCE(content_hash, ''), duration_seconds FROM chat_video_attachments WHERE id = $1`, attachmentID).Scan(&contentHash, &duration)
	if contentHash != "" {
		_, _ = config.DB.Exec(config.Ctx, `
			INSERT INTO video_analysis_cache (content_hash, has_audio, audio_transcript, frame_count, frame_analysis, context_text, original_duration)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)
			ON CONFLICT (content_hash) DO NOTHING`,
			contentHash, videoResult.HasAudio, videoResult.AudioTranscript, videoResult.FrameCount,
			string(framesJSON), contextText, duration)
	}
}

func markVideoError(attachmentID, errMsg string) {
	_, err := config.DB.Exec(config.Ctx, `
		UPDATE chat_video_attachments SET status = 'error', error_message = $1 WHERE id = $2`,
		errMsg, attachmentID)
	if err != nil {
		log.Printf("[VideoAttachment] Failed to mark error id=%s: %v", attachmentID, err)
	}
}

func isAllowedVideoMime(mimeType string) bool {
	switch mimeType {
	case "video/mp4", "video/webm", "video/quicktime":
		return true
	default:
		return false
	}
}

func probeVideoDuration(path string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path)
	output, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			// ffprobe not installed; skip duration check, processing service will validate
			return 0, nil
		}
		return 0, fmt.Errorf("ffprobe failed: %w", err)
	}
	val, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse duration: %w", err)
	}
	return int(val), nil
}

func loadChatVideoAttachmentForPrompt(userID, sessionID, attachmentID string) (string, error) {
	if attachmentID == "" {
		return "", nil
	}
	if _, err := uuid.Parse(attachmentID); err != nil {
		return "", fmt.Errorf("invalid video attachment id")
	}

	var status, contextText string
	var expiresAt *time.Time
	err := config.DB.QueryRow(config.Ctx, `
		SELECT status, context_text, expires_at FROM chat_video_attachments
		WHERE id = $1 AND user_id = $2 AND session_id = $3`,
		attachmentID, userID, sessionID).Scan(&status, &contextText, &expiresAt)
	if err != nil {
		return "", fmt.Errorf("video attachment not found")
	}
	if expiresAt != nil && time.Now().After(*expiresAt) {
		return "", fmt.Errorf("video attachment has expired")
	}
	if status != "done" {
		return "", fmt.Errorf("video attachment is not ready (status: %s)", status)
	}
	return contextText, nil
}

func buildChatVideoContext(contextText string) string {
	if strings.TrimSpace(contextText) == "" {
		return ""
	}
	if len(contextText) > maxVideoContextChars {
		contextText = contextText[:maxVideoContextChars]
	}
	return "[CHAT VIDEO ANALYSIS CONTEXT]\n" + contextText + "\n[END CHAT VIDEO ANALYSIS CONTEXT]\n"
}
