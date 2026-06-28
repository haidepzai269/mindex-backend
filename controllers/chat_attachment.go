package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	maxChatImageUploadBytes           int64 = 8 << 20
	maxChatImageAttachmentsPerMessage       = 3
	maxStoredChatImageOCRChars              = 30000
	maxChatImageOCRCharsPerAttachment       = 6000
	maxChatImageOCRContextChars             = 12000
)

type ChatAttachmentOverride struct {
	AttachmentID string `json:"attachment_id"`
	OCRText      string `json:"ocr_text"`
}

type chatAttachmentUploadRequest struct {
	SessionID    string
	DocumentID   string
	CollectionID string
}

type chatImageOCRResult struct {
	Text    string          `json:"text"`
	Preview string          `json:"preview"`
	Blocks  json.RawMessage `json:"blocks"`
}

type chatImageAttachmentRecord struct {
	ID              string          `json:"id"`
	URL             string          `json:"url"`
	Filename        string          `json:"filename"`
	MimeType        string          `json:"mime_type"`
	SizeBytes       int64           `json:"size_bytes"`
	Width           int             `json:"width"`
	Height          int             `json:"height"`
	Status          string          `json:"status"`
	OCRText         string          `json:"ocr_text,omitempty"`
	OCRPreview      string          `json:"ocr_preview,omitempty"`
	OCRBlocks       json.RawMessage `json:"ocr_blocks,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	StoragePublicID string          `json:"-"`
}

// UploadChatImageAttachment handles image-only chat attachments. It does not create documents
// or enqueue the document RAG pipeline.
func UploadChatImageAttachment(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxChatImageUploadBytes+(1<<20))

	req := chatAttachmentUploadRequest{
		SessionID:    strings.TrimSpace(c.PostForm("session_id")),
		DocumentID:   strings.TrimSpace(c.PostForm("document_id")),
		CollectionID: strings.TrimSpace(c.PostForm("collection_id")),
	}
	if (req.DocumentID == "" && req.CollectionID == "") || (req.DocumentID != "" && req.CollectionID != "") {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "MISSING_SCOPE", "message": "Provide exactly one document_id or collection_id"})
		return
	}

	userID := c.GetString("user_id")
	sessionID, err := ensureChatAttachmentSession(c.Request.Context(), userID, req)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, pgx.ErrNoRows) {
			status = http.StatusNotFound
		}
		if errors.Is(err, errChatAttachmentExpired) {
			status = http.StatusForbidden
		}
		c.JSON(status, gin.H{"success": false, "error": "SESSION_PREPARE_FAILED", "message": err.Error()})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "MISSING_FILE", "message": "Image file is required"})
		return
	}
	if fileHeader.Size <= 0 || fileHeader.Size > maxChatImageUploadBytes {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "IMAGE_TOO_LARGE", "message": "Image must be smaller than 8MB"})
		return
	}

	attachmentID := uuid.New().String()
	tmpDir := filepath.Join(os.TempDir(), "mindex-chat-images")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "TEMP_DIR_FAILED", "message": "Cannot prepare upload directory"})
		return
	}
	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if ext == "" {
		ext = ".img"
	}
	tmpPath := filepath.Join(tmpDir, attachmentID+ext)
	defer os.Remove(tmpPath)

	if err := c.SaveUploadedFile(fileHeader, tmpPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "SAVE_UPLOAD_FAILED", "message": "Cannot save image"})
		return
	}

	mimeType, err := detectChatImageMime(tmpPath)
	if err != nil || !isAllowedChatImageMime(mimeType) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "UNSUPPORTED_IMAGE", "message": "Only PNG, JPEG, and WebP images are supported"})
		return
	}

	width, height := readChatImageSize(tmpPath)
	publicID := fmt.Sprintf("mindex_chat_images/%s/%s", userID, attachmentID)
	upload, err := utils.UploadImageToCloudinary(tmpPath, publicID)
	if err != nil {
		log.Printf("[ChatAttachment] Cloudinary upload failed user=%s session=%s: %v", userID, sessionID, err)
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "IMAGE_UPLOAD_FAILED", "message": "Cannot upload image"})
		return
	}

	ocrResult, ocrErr := runChatImageOCR(upload.SecureURL)
	status := "done"
	errorMessage := ""
	if ocrErr != nil {
		status = "error"
		errorMessage = compactChatPreview(ocrErr.Error(), 400)
		log.Printf("[ChatAttachment] OCR failed attachment=%s user=%s: %v", attachmentID, userID, ocrErr)
	}

	ocrText := truncateRunes(strings.TrimSpace(ocrResult.Text), maxStoredChatImageOCRChars)
	ocrText = strings.ToValidUTF8(ocrText, "")
	ocrPreview := compactChatPreview(ocrText, 1200)
	if ocrPreview == "" {
		ocrPreview = compactChatPreview(ocrResult.Preview, 1200)
	}
	ocrPreview = strings.ToValidUTF8(ocrPreview, "")
	ocrBlocks := ocrResult.Blocks
	if len(ocrBlocks) == 0 {
		ocrBlocks = json.RawMessage("[]")
	}
	if !json.Valid(ocrBlocks) {
		ocrBlocks = json.RawMessage("[]")
	}

	if err := insertChatImageAttachment(c.Request.Context(), userID, sessionID, req, chatImageAttachmentRecord{
		ID:              attachmentID,
		URL:             upload.SecureURL,
		Filename:        fileHeader.Filename,
		MimeType:        mimeType,
		SizeBytes:       fileHeader.Size,
		Width:           width,
		Height:          height,
		Status:          status,
		OCRText:         ocrText,
		OCRPreview:      ocrPreview,
		OCRBlocks:       ocrBlocks,
		ErrorMessage:    errorMessage,
		StoragePublicID: upload.PublicID,
	}); err != nil {
		_ = utils.DestroyImageFromCloudinary(upload.PublicID)
		log.Printf("[ChatAttachment] DB insert failed attachment=%s user=%s: %v", attachmentID, userID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "ATTACHMENT_SAVE_FAILED", "message": "Cannot save image attachment"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"id":            attachmentID,
			"attachment_id": attachmentID,
			"session_id":    sessionID,
			"url":           upload.SecureURL,
			"filename":      fileHeader.Filename,
			"mime_type":     mimeType,
			"size_bytes":    fileHeader.Size,
			"width":         width,
			"height":        height,
			"status":        status,
			"error_message": errorMessage,
			"ocr_text":      ocrText,
			"ocr_preview":   ocrPreview,
			"ocr_blocks":    ocrBlocks,
		},
	})
}

var errChatAttachmentExpired = errors.New("target is expired")

func ensureChatAttachmentSession(ctx context.Context, userID string, req chatAttachmentUploadRequest) (string, error) {
	if req.CollectionID != "" {
		var name string
		if err := config.DB.QueryRow(ctx, `SELECT name FROM collections WHERE id = $1`, req.CollectionID).Scan(&name); err != nil {
			return "", err
		}
	} else {
		var title string
		var expiredAt *time.Time
		if err := config.DB.QueryRow(ctx, `SELECT title, expired_at FROM documents WHERE id = $1`, req.DocumentID).Scan(&title, &expiredAt); err != nil {
			return "", err
		}
		if expiredAt != nil && expiredAt.Before(time.Now()) {
			return "", errChatAttachmentExpired
		}
	}

	if req.SessionID != "" {
		var exists bool
		var err error
		if req.CollectionID != "" {
			err = config.DB.QueryRow(ctx, `
				SELECT EXISTS(SELECT 1 FROM chat_histories WHERE session_id = $1 AND collection_id = $2 AND user_id = $3 AND chat_scope = $4)`,
				req.SessionID, req.CollectionID, userID, normalChatScope).Scan(&exists)
		} else {
			err = config.DB.QueryRow(ctx, `
				SELECT EXISTS(SELECT 1 FROM chat_histories WHERE session_id = $1 AND document_id = $2 AND user_id = $3 AND chat_scope = $4)`,
				req.SessionID, req.DocumentID, userID, normalChatScope).Scan(&exists)
		}
		if err != nil {
			return "", err
		}
		if exists {
			return req.SessionID, nil
		}
	}

	sessionID := uuid.New().String()
	var err error
	if req.CollectionID != "" {
		_, err = config.DB.Exec(ctx, `
			INSERT INTO chat_histories (user_id, collection_id, session_id, full_messages, started_at, chat_scope)
			VALUES ($1, $2, $3, '[]'::jsonb, NOW(), $4)`,
			userID, req.CollectionID, sessionID, normalChatScope)
	} else {
		_, err = config.DB.Exec(ctx, `
			INSERT INTO chat_histories (user_id, document_id, session_id, full_messages, started_at, chat_scope)
			VALUES ($1, $2, $3, '[]'::jsonb, NOW(), $4)`,
			userID, req.DocumentID, sessionID, normalChatScope)
	}
	if err != nil {
		return "", err
	}
	return sessionID, nil
}

func insertChatImageAttachment(ctx context.Context, userID, sessionID string, req chatAttachmentUploadRequest, attachment chatImageAttachmentRecord) error {
	var documentID interface{}
	var collectionID interface{}
	if req.DocumentID != "" {
		documentID = req.DocumentID
	}
	if req.CollectionID != "" {
		collectionID = req.CollectionID
	}

	_, err := config.DB.Exec(ctx, `
		INSERT INTO chat_image_attachments
			(id, user_id, session_id, document_id, collection_id, original_name, mime_type, size_bytes, width, height,
			 storage_url, storage_public_id, status, error_message, ocr_text, ocr_preview, ocr_blocks)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NULLIF($14, ''), $15, $16, $17::jsonb)`,
		attachment.ID, userID, sessionID, documentID, collectionID, attachment.Filename, attachment.MimeType, attachment.SizeBytes,
		attachment.Width, attachment.Height, attachment.URL, attachment.StoragePublicID, attachment.Status, attachment.ErrorMessage,
		attachment.OCRText, attachment.OCRPreview, string(attachment.OCRBlocks))
	return err
}

func detectChatImageMime(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	header := make([]byte, 512)
	n, err := io.ReadFull(file, header)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", err
	}
	return http.DetectContentType(header[:n]), nil
}

func isAllowedChatImageMime(mimeType string) bool {
	switch strings.ToLower(mimeType) {
	case "image/png", "image/jpeg", "image/webp":
		return true
	default:
		return false
	}
}

func readChatImageSize(path string) (int, int) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer file.Close()

	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func runChatImageOCR(imageURL string) (chatImageOCRResult, error) {
	if utils.Processing == nil {
		return chatImageOCRResult{}, errors.New("processing client not initialized")
	}

	raw, err := utils.Processing.CallSync("ocr", map[string]any{"image_url": imageURL}, 45*time.Second)
	if err != nil {
		return chatImageOCRResult{}, fmt.Errorf("ocr processing failed: %w", err)
	}

	var result chatImageOCRResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return chatImageOCRResult{}, fmt.Errorf("parse ocr result: %w", err)
	}
	if len(result.Blocks) == 0 {
		result.Blocks = json.RawMessage("[]")
	}
	return result, nil
}

func loadChatImageAttachmentsForPrompt(ctx context.Context, userID, sessionID string, attachmentIDs []string, overrides []ChatAttachmentOverride) ([]chatImageAttachmentRecord, error) {
	if len(attachmentIDs) == 0 {
		return nil, nil
	}
	if len(attachmentIDs) > maxChatImageAttachmentsPerMessage {
		return nil, fmt.Errorf("too many image attachments")
	}

	overrideText := map[string]string{}
	for _, override := range overrides {
		id := strings.TrimSpace(override.AttachmentID)
		if id == "" {
			continue
		}
		overrideText[id] = truncateRunes(strings.TrimSpace(override.OCRText), maxStoredChatImageOCRChars)
	}

	seen := map[string]bool{}
	attachments := make([]chatImageAttachmentRecord, 0, len(attachmentIDs))
	for _, rawID := range attachmentIDs {
		attachmentID := strings.TrimSpace(rawID)
		if attachmentID == "" || seen[attachmentID] {
			continue
		}
		if _, err := uuid.Parse(attachmentID); err != nil {
			return nil, fmt.Errorf("invalid image attachment id")
		}
		seen[attachmentID] = true

		var att chatImageAttachmentRecord
		var blocks []byte
		err := config.DB.QueryRow(ctx, `
			SELECT id::text, storage_url, original_name, mime_type, size_bytes, width, height, status,
			       ocr_text, ocr_preview, ocr_blocks, COALESCE(error_message, '')
			FROM chat_image_attachments
			WHERE id = $1 AND user_id = $2 AND session_id = $3`,
			attachmentID, userID, sessionID).Scan(
			&att.ID, &att.URL, &att.Filename, &att.MimeType, &att.SizeBytes, &att.Width, &att.Height,
			&att.Status, &att.OCRText, &att.OCRPreview, &blocks, &att.ErrorMessage,
		)
		if err != nil {
			return nil, fmt.Errorf("image attachment not found")
		}
		if att.Status != "done" {
			return nil, fmt.Errorf("image attachment is not ready")
		}
		att.OCRBlocks = json.RawMessage(blocks)
		if len(att.OCRBlocks) == 0 {
			att.OCRBlocks = json.RawMessage("[]")
		}

		if override, ok := overrideText[att.ID]; ok {
			att.OCRText = override
			att.OCRPreview = compactChatPreview(override, 1200)
		}
		attachments = append(attachments, att)
	}
	return attachments, nil
}

func buildChatImageOCRContext(attachments []chatImageAttachmentRecord) string {
	if len(attachments) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("[CHAT IMAGE OCR CONTEXT]\n")
	remaining := maxChatImageOCRContextChars
	for index, att := range attachments {
		if remaining <= 0 {
			break
		}
		text := strings.TrimSpace(att.OCRText)
		if text == "" {
			text = "(OCR found no readable text in this image. Do not pretend to inspect visual details; explain that only OCR text is available.)"
		}
		text = truncateRunes(text, minInt(maxChatImageOCRCharsPerAttachment, remaining))
		remaining -= len([]rune(text))

		builder.WriteString(fmt.Sprintf("Image %d: %s\n", index+1, att.Filename))
		builder.WriteString(text)
		builder.WriteString("\n\n")
	}
	builder.WriteString("[END CHAT IMAGE OCR CONTEXT]\n")
	return builder.String()
}

func chatImageAttachmentsForMessage(attachments []chatImageAttachmentRecord) []gin.H {
	if len(attachments) == 0 {
		return nil
	}

	items := make([]gin.H, 0, len(attachments))
	for _, att := range attachments {
		items = append(items, gin.H{
			"id":          att.ID,
			"url":         att.URL,
			"filename":    att.Filename,
			"mime_type":   att.MimeType,
			"size_bytes":  att.SizeBytes,
			"width":       att.Width,
			"height":      att.Height,
			"status":      att.Status,
			"ocr_preview": att.OCRPreview,
			"ocr_text":    att.OCRText,
			"ocr_blocks":  att.OCRBlocks,
		})
	}
	return items
}

func updateChatImageAttachmentMessageID(ctx context.Context, userID, sessionID, messageID string, attachments []chatImageAttachmentRecord) {
	for _, att := range attachments {
		if _, err := config.DB.Exec(ctx, `
			UPDATE chat_image_attachments
			SET message_id = $1
			WHERE id = $2 AND user_id = $3 AND session_id = $4`,
			messageID, att.ID, userID, sessionID); err != nil {
			log.Printf("[ChatAttachment] Failed to attach image %s to message %s: %v", att.ID, messageID, err)
		}
	}
}

func buildChatHistoryQuestion(question string, attachments []chatImageAttachmentRecord) string {
	if len(attachments) == 0 {
		return question
	}

	previews := make([]string, 0, len(attachments))
	for i, att := range attachments {
		preview := strings.TrimSpace(att.OCRPreview)
		if preview == "" {
			preview = "(no OCR text)"
		}
		previews = append(previews, fmt.Sprintf("Image %d OCR: %s", i+1, compactChatPreview(preview, 500)))
	}
	return strings.TrimSpace(question + "\n" + strings.Join(previews, "\n"))
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
