package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"mindex-backend/config"
	"mindex-backend/internal/ws"
	"mindex-backend/models"
	"mindex-backend/utils"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	maxDocumentUploadSize = int64(50 * 1024 * 1024)
	maxMultipartBodySize  = maxDocumentUploadSize + int64(1024*1024)
	uploadJobMaxAttempts  = 3
)

var safeFilenamePattern = regexp.MustCompile(`[^a-zA-Z0-9._ -]+`)

type uploadFileInfo struct {
	FileName  string
	Extension string
	MimeType  string
}

type uploadValidationError struct {
	Status  int
	Code    string
	Message string
}

func (e *uploadValidationError) Error() string {
	return e.Message
}

type existingDocument struct {
	ID                 string
	Status             string
	ExpiredAt          *time.Time
	CloudinaryPublicID *string
}

func PresignUpload(c *gin.Context) {
	signature, timestamp, apiKey, uploadUrl := utils.GenerateCloudinarySignature()

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"signature":  signature,
			"timestamp":  timestamp,
			"api_key":    apiKey,
			"upload_url": uploadUrl,
		},
	})
}

func InitiateUpload(c *gin.Context) {
	userID := c.GetString("user_id")
	userPersona := c.GetString("persona")

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxMultipartBodySize)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		status := http.StatusBadRequest
		message := "Khong nhan duoc file"
		code := "FILE_REQUIRED"
		if strings.Contains(err.Error(), "request body too large") {
			status = http.StatusRequestEntityTooLarge
			message = "File vuot qua gioi han 50MB"
			code = "FILE_TOO_LARGE"
		}
		c.JSON(status, gin.H{"success": false, "error": code, "message": message})
		return
	}

	fileInfo, err := validateUploadedDocument(fileHeader)
	if err != nil {
		var validationErr *uploadValidationError
		if errors.As(err, &validationErr) {
			c.JSON(validationErr.Status, gin.H{"success": false, "error": validationErr.Code, "message": validationErr.Message})
			return
		}
		c.JSON(400, gin.H{"success": false, "error": "INVALID_FILE", "message": err.Error()})
		return
	}

	roomID := strings.TrimSpace(c.PostForm("room_id"))
	if roomID != "" && !IsRoomMember(roomID, userID) {
		c.JSON(403, gin.H{"success": false, "error": "NOT_ROOM_MEMBER", "message": "Ban khong co quyen upload vao phong nay"})
		return
	}

	docID := uuid.New().String()
	localPath, fileHash, err := saveUploadedDocument(fileHeader, fileInfo.FileName, docID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": "TEMP_SAVE_FAILED", "message": "Khong the luu file tam"})
		return
	}

	existing, found, err := findExistingDocumentByHash(fileHash)
	if err != nil {
		_ = os.Remove(localPath)
		c.JSON(500, gin.H{"success": false, "error": "DEDUP_CHECK_FAILED", "message": "Khong the kiem tra tai lieu trung lap"})
		return
	}
	if found {
		if existing.ExpiredAt != nil && existing.ExpiredAt.Before(time.Now()) {
			if err := deleteExpiredDuplicate(existing); err != nil {
				_ = os.Remove(localPath)
				c.JSON(500, gin.H{"success": false, "error": "EXPIRED_DUPLICATE_CLEANUP_FAILED", "message": "Khong the lam moi tai lieu da het han"})
				return
			}
		} else {
			_ = os.Remove(localPath)
			handleDuplicateDocument(c, existing, roomID, userID)
			return
		}
	}

	if roomID != "" {
		if err := ensureRoomDocQuota(roomID, userID); err != nil {
			_ = os.Remove(localPath)
			writeUploadError(c, err)
			return
		}
	}

	publicID := fmt.Sprintf("mindex_uploads/%s%s", docID, fileInfo.Extension)
	cloudinaryUpload, err := utils.UploadRawToCloudinary(localPath, publicID)
	if err != nil {
		_ = os.Remove(localPath)
		log.Printf("[Upload] Cloudinary upload failed: %v", err)
		c.JSON(502, gin.H{"success": false, "error": "CLOUDINARY_UPLOAD_FAILED", "message": "Khong the luu file goc len Cloudinary"})
		return
	}

	if err := createDocumentAndUploadJob(docID, userID, fileInfo.FileName, userPersona, fileHash, roomID, localPath, cloudinaryUpload); err != nil {
		_ = os.Remove(localPath)
		_ = utils.DestroyRawFromCloudinary(cloudinaryUpload.PublicID)

		if errors.Is(err, pgx.ErrNoRows) {
			existing, found, findErr := findExistingDocumentByHash(fileHash)
			if findErr == nil && found {
				handleDuplicateDocument(c, existing, roomID, userID)
				return
			}
		}

		log.Printf("[Upload] DB create failed: %v", err)
		c.JSON(500, gin.H{"success": false, "error": "DOCUMENT_CREATE_FAILED", "message": "Khong the tao tai lieu trong DB"})
		return
	}

	utils.UpdateDocProgressDetail(docID, "queued", 0, "Tai lieu dang cho xu ly", "")
	signalUploadWorker(docID)
	broadcastRoomUpload(roomID, userID, fileInfo.FileName)
	go CheckAndAwardBadges(userID)

	c.JSON(http.StatusAccepted, gin.H{
		"success": true,
		"data": gin.H{
			"document_id":  docID,
			"status":       "queued",
			"is_duplicate": false,
			"message":      "Tai len thanh cong, dang bat dau xu ly...",
		},
	})
}

func GetProcessingStatus(c *gin.Context) {
	docID := c.Param("id")

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Transfer-Encoding", "chunked")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(500, gin.H{"error": "Streaming unsupported"})
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
			payload := getProgressPayload(docID)
			fmt.Fprintf(c.Writer, "data: %s\n\n", payload)
			flusher.Flush()

			var res map[string]interface{}
			_ = json.Unmarshal([]byte(payload), &res)
			if res["status"] == "ready" || res["status"] == "error" {
				return
			}
		}
	}
}

func validateUploadedDocument(fileHeader *multipart.FileHeader) (uploadFileInfo, error) {
	fileName := sanitizeUploadFilename(fileHeader.Filename)
	if fileName == "" {
		return uploadFileInfo{}, &uploadValidationError{Status: 400, Code: "INVALID_FILENAME", Message: "Ten file khong hop le"}
	}
	return validateDocumentSignature(fileName, fileHeader.Size, func() ([]byte, error) {
		file, err := fileHeader.Open()
		if err != nil {
			return nil, err
		}
		defer file.Close()
		header := make([]byte, 8)
		n, err := io.ReadFull(file, header)
		if err != nil && err != io.ErrUnexpectedEOF {
			return nil, err
		}
		return header[:n], nil
	})
}

func validateDocumentSignature(fileName string, size int64, readHeader func() ([]byte, error)) (uploadFileInfo, error) {
	if size <= 0 {
		return uploadFileInfo{}, &uploadValidationError{Status: 400, Code: "EMPTY_FILE", Message: "File rong hoac khong hop le"}
	}
	if size > maxDocumentUploadSize {
		return uploadFileInfo{}, &uploadValidationError{Status: http.StatusRequestEntityTooLarge, Code: "FILE_TOO_LARGE", Message: "File vuot qua gioi han 50MB"}
	}

	ext := strings.ToLower(filepath.Ext(fileName))
	header, err := readHeader()
	if err != nil {
		return uploadFileInfo{}, fmt.Errorf("khong the doc chu ky file: %w", err)
	}

	switch ext {
	case ".pdf":
		if len(header) < 4 || string(header[:4]) != "%PDF" {
			return uploadFileInfo{}, &uploadValidationError{Status: http.StatusUnsupportedMediaType, Code: "INVALID_FILE_SIGNATURE", Message: "File PDF khong hop le"}
		}
		return uploadFileInfo{FileName: fileName, Extension: ext, MimeType: "application/pdf"}, nil
	case ".docx":
		if len(header) < 4 || string(header[:4]) != "PK\x03\x04" {
			return uploadFileInfo{}, &uploadValidationError{Status: http.StatusUnsupportedMediaType, Code: "INVALID_FILE_SIGNATURE", Message: "File DOCX khong hop le"}
		}
		return uploadFileInfo{FileName: fileName, Extension: ext, MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"}, nil
	default:
		return uploadFileInfo{}, &uploadValidationError{Status: http.StatusUnsupportedMediaType, Code: "UNSUPPORTED_FILE_TYPE", Message: "Chi ho tro PDF hoac DOCX"}
	}
}

func sanitizeUploadFilename(raw string) string {
	name := filepath.Base(strings.TrimSpace(raw))
	name = strings.ReplaceAll(name, "\x00", "")
	name = safeFilenamePattern.ReplaceAllString(name, "_")
	name = strings.Join(strings.Fields(name), " ")
	if name == "." || name == "/" || name == "\\" {
		return ""
	}
	if len(name) > 180 {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if len(base) > 160 {
			base = base[:160]
		}
		name = base + ext
	}
	return name
}

func saveUploadedDocument(fileHeader *multipart.FileHeader, fileName string, docID string) (string, string, error) {
	uploadDir := "./tmp/uploads"
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return "", "", err
	}

	localPath := filepath.Join(uploadDir, fmt.Sprintf("%s-%s", docID, fileName))
	src, err := fileHeader.Open()
	if err != nil {
		return "", "", err
	}
	defer src.Close()

	dst, err := os.Create(localPath)
	if err != nil {
		return "", "", err
	}
	defer dst.Close()

	hash := sha256.New()
	if _, err := io.Copy(dst, io.TeeReader(src, hash)); err != nil {
		_ = os.Remove(localPath)
		return "", "", err
	}

	return localPath, hex.EncodeToString(hash.Sum(nil)), nil
}

func findExistingDocumentByHash(fileHash string) (existingDocument, bool, error) {
	var doc existingDocument
	err := config.DB.QueryRow(config.Ctx, `
		SELECT id, status, expired_at, cloudinary_public_id
		FROM documents
		WHERE file_hash = $1
		LIMIT 1`, fileHash).Scan(&doc.ID, &doc.Status, &doc.ExpiredAt, &doc.CloudinaryPublicID)
	if errors.Is(err, pgx.ErrNoRows) {
		return existingDocument{}, false, nil
	}
	if err != nil {
		return existingDocument{}, false, err
	}
	return doc, true, nil
}

func deleteExpiredDuplicate(doc existingDocument) error {
	_, err := config.DB.Exec(config.Ctx, `DELETE FROM documents WHERE id = $1`, doc.ID)
	if err != nil {
		return err
	}
	if doc.CloudinaryPublicID != nil {
		go func(publicID string) {
			if err := utils.DestroyRawFromCloudinary(publicID); err != nil {
				log.Printf("[Upload] Cloudinary cleanup failed for expired duplicate %s: %v", publicID, err)
			}
		}(*doc.CloudinaryPublicID)
	}
	return nil
}

func createDocumentAndUploadJob(docID, userID, title, persona, fileHash, roomID, localPath string, upload *utils.CloudinaryUploadResult) error {
	tx, err := config.DB.Begin(config.Ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(config.Ctx)

	if roomID != "" {
		err = tx.QueryRow(config.Ctx, `
			INSERT INTO documents (id, user_id, title, cloudinary_url, cloudinary_public_id, status, creator_persona, expired_at, file_hash, room_id)
			VALUES ($1, $2, $3, $4, $5, 'queued', $6, NULL, $7, $8)
			ON CONFLICT (file_hash) DO NOTHING
			RETURNING id`,
			docID, userID, title, upload.SecureURL, upload.PublicID, persona, fileHash, roomID,
		).Scan(&docID)
	} else {
		err = tx.QueryRow(config.Ctx, `
			INSERT INTO documents (id, user_id, title, cloudinary_url, cloudinary_public_id, status, creator_persona, expired_at, file_hash)
			VALUES ($1, $2, $3, $4, $5, 'queued', $6, NOW() + INTERVAL '24 hours', $7)
			ON CONFLICT (file_hash) DO NOTHING
			RETURNING id`,
			docID, userID, title, upload.SecureURL, upload.PublicID, persona, fileHash,
		).Scan(&docID)
	}
	if err != nil {
		return err
	}

	if _, err = tx.Exec(config.Ctx, `
		INSERT INTO document_references (user_id, document_id, is_owner, pinned)
		VALUES ($1, $2, TRUE, FALSE)
		ON CONFLICT (user_id, document_id) DO NOTHING`, userID, docID); err != nil {
		return err
	}

	if _, err = tx.Exec(config.Ctx, `
		INSERT INTO upload_jobs (document_id, user_id, local_path, cloudinary_url, cloudinary_public_id, status, attempts, max_attempts, next_run_at)
		VALUES ($1, $2, $3, $4, $5, 'queued', 0, $6, NOW())
		ON CONFLICT (document_id) DO UPDATE SET
			local_path = EXCLUDED.local_path,
			cloudinary_url = EXCLUDED.cloudinary_url,
			cloudinary_public_id = EXCLUDED.cloudinary_public_id,
			status = 'queued',
			attempts = 0,
			next_run_at = NOW(),
			error_code = NULL,
			error_message = NULL,
			updated_at = NOW()`,
		docID, userID, localPath, upload.SecureURL, upload.PublicID, uploadJobMaxAttempts); err != nil {
		return err
	}

	if roomID != "" {
		_, _ = tx.Exec(config.Ctx, `UPDATE group_room_members SET doc_count = doc_count + 1 WHERE room_id = $1 AND user_id = $2`, roomID, userID)
	}

	return tx.Commit(config.Ctx)
}

func handleDuplicateDocument(c *gin.Context, doc existingDocument, roomID, userID string) {
	if roomID != "" {
		if err := linkDuplicateDocumentToRoom(roomID, userID, doc.ID); err != nil {
			writeUploadError(c, err)
			return
		}
		broadcastRoomLinked(roomID, userID, doc.ID)
	}

	if _, err := config.DB.Exec(config.Ctx, `
		INSERT INTO document_references (user_id, document_id, is_owner, pinned)
		VALUES ($1, $2, FALSE, FALSE)
		ON CONFLICT (user_id, document_id) DO NOTHING`, userID, doc.ID); err != nil {
		c.JSON(500, gin.H{"success": false, "error": "REFERENCE_CREATE_FAILED", "message": "Khong the them tai lieu vao thu vien"})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"document_id":  doc.ID,
			"status":       doc.Status,
			"is_duplicate": true,
			"message":      "Tai lieu da ton tai, da duoc them vao thu vien cua ban.",
		},
	})
}

func linkDuplicateDocumentToRoom(roomID, userID, docID string) error {
	var alreadyLinked bool
	if err := config.DB.QueryRow(config.Ctx, `
		SELECT EXISTS(
			SELECT 1 FROM documents WHERE id = $2 AND room_id = $1
			UNION
			SELECT 1 FROM group_room_doc_links WHERE room_id = $1 AND document_id = $2
		)`, roomID, docID).Scan(&alreadyLinked); err != nil {
		return fmt.Errorf("ROOM_LINK_CHECK_FAILED:%w", err)
	}
	if alreadyLinked {
		return nil
	}

	if err := ensureRoomDocQuota(roomID, userID); err != nil {
		return err
	}

	_, err := config.DB.Exec(config.Ctx, `
		INSERT INTO group_room_doc_links (room_id, document_id, linked_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (room_id, document_id) DO NOTHING`, roomID, docID, userID)
	return err
}

func ensureRoomDocQuota(roomID, userID string) error {
	var docCount int
	if err := config.DB.QueryRow(config.Ctx, `
		SELECT COUNT(*) FROM (
			SELECT d.id FROM documents d WHERE d.room_id = $1 AND d.user_id = $2
			UNION
			SELECT l.document_id FROM group_room_doc_links l WHERE l.room_id = $1 AND l.linked_by = $2
		) sub`, roomID, userID).Scan(&docCount); err != nil {
		return fmt.Errorf("ROOM_DOC_COUNT_FAILED:%w", err)
	}
	if docCount >= 3 {
		return &uploadValidationError{Status: 403, Code: "ROOM_DOC_LIMIT", Message: "Toi da 3 tai lieu/nguoi trong mot phong."}
	}
	return nil
}

func signalUploadWorker(docID string) {
	if config.RedisClient == nil {
		return
	}
	if err := config.RedisClient.RPush(config.Ctx, config.Env.RedisQueueName, docID).Err(); err != nil {
		log.Printf("[Upload] Redis signal failed for doc %s: %v", docID, err)
	}
}

func broadcastRoomUpload(roomID, userID, filename string) {
	if roomID == "" {
		return
	}
	var userName string
	_ = config.DB.QueryRow(config.Ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&userName)
	ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
		Type:   "doc_uploaded",
		RoomID: roomID,
		UserID: userID,
		Payload: gin.H{
			"user_name": userName,
			"doc_name":  filename,
		},
	})
}

func broadcastRoomLinked(roomID, userID, docID string) {
	if roomID == "" {
		return
	}
	var userName, docTitle string
	_ = config.DB.QueryRow(config.Ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&userName)
	_ = config.DB.QueryRow(config.Ctx, `SELECT title FROM documents WHERE id = $1`, docID).Scan(&docTitle)
	ws.RoomHubInstance.BroadcastToRoom(roomID, models.RoomEvent{
		Type:   "doc_linked",
		RoomID: roomID,
		UserID: userID,
		Payload: gin.H{
			"user_name": userName,
			"doc_name":  docTitle,
			"doc_id":    docID,
		},
	})
}

func writeUploadError(c *gin.Context, err error) {
	var validationErr *uploadValidationError
	if errors.As(err, &validationErr) {
		c.JSON(validationErr.Status, gin.H{"success": false, "error": validationErr.Code, "message": validationErr.Message})
		return
	}
	log.Printf("[Upload] error: %v", err)
	c.JSON(500, gin.H{"success": false, "error": "UPLOAD_FAILED", "message": "Khong the upload tai lieu"})
}

func getProgressPayload(docID string) string {
	if config.RedisClient != nil {
		val, err := config.RedisClient.Get(config.Ctx, "doc_progress:"+docID).Result()
		if err == nil && val != "" {
			return val
		}
	}

	payload := fallbackProgressFromDB(docID)
	data, _ := json.Marshal(payload)
	return string(data)
}

func fallbackProgressFromDB(docID string) gin.H {
	var status string
	var errorCode, errorMessage, jobStatus *string
	err := config.DB.QueryRow(config.Ctx, `
		SELECT d.status, d.processing_error_code, d.processing_error_message, uj.status
		FROM documents d
		LEFT JOIN upload_jobs uj ON uj.document_id = d.id
		WHERE d.id = $1`, docID).Scan(&status, &errorCode, &errorMessage, &jobStatus)
	if err != nil {
		return gin.H{"status": "pending", "progress": 0, "message": "Dang cho xu ly"}
	}

	progress := 0
	message := "Dang cho xu ly"
	switch status {
	case "ready":
		progress = 100
		message = "Tai lieu da san sang"
	case "processing":
		progress = 20
		message = "Dang xu ly tai lieu"
	case "error":
		message = "Xu ly tai lieu that bai"
	case "queued":
		if jobStatus != nil && *jobStatus == "retrying" {
			message = "Dang thu lai xu ly tai lieu"
		}
	}
	if errorMessage != nil && *errorMessage != "" {
		message = *errorMessage
	}

	payload := gin.H{"status": status, "progress": progress, "message": message}
	if errorCode != nil && *errorCode != "" {
		payload["error_code"] = *errorCode
	}
	return payload
}
