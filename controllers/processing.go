package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mindex-backend/config"
	"mindex-backend/utils"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type UploadRequest struct {
	CloudinaryURL string `json:"cloudinary_url" binding:"required"`
	Filename      string `json:"filename" binding:"required"`
	FileSize      int64  `json:"file_size" binding:"required"`
	MimeType      string `json:"mime_type" binding:"required"`
}

type UploadJob struct {
	DocID     string `json:"doc_id"`
	LocalPath string `json:"local_path"`
	UserID    string `json:"user_id"`
}

// Lấy signature để client upload lên Cloudinary
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

// Khởi tạo tiến trình xử lý tài liệu AI vào Redis Queue
func InitiateUpload(c *gin.Context) {
	userID := c.GetString("user_id")
	userPersona := c.GetString("persona")

	// 1. Nhận file từ Multipart Form
	fileHeader, err := c.FormFile("file")
	if err != nil {
		fmt.Printf("❌ Lỗi nhận file: %v\n", err)
		c.JSON(400, gin.H{"success": false, "message": "Không nhận được file", "debug": err.Error()})
		return
	}

	// 1.1. Tính SHA-256 Hash để Deduplication
	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể mở file để tính hash"})
		return
	}
	defer f.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Lỗi khi tính toán file hash"})
		return
	}
	fileHash := hex.EncodeToString(hash.Sum(nil))

	// 1.2. Kiểm tra xem file này đã tồn tại trong hệ thống chưa
	var existingDocID string
	var currentStatus string
	var expiredAt *time.Time
	err = config.DB.QueryRow(config.Ctx, 
		"SELECT id, status, expired_at FROM documents WHERE file_hash = $1 LIMIT 1", 
		fileHash,
	).Scan(&existingDocID, &currentStatus, &expiredAt)

	if err == nil && existingDocID != "" {
		// KIỂM TRA HẾT HẠN: Nếu đã hết hạn -> Xóa bản cũ rác và cho phép up mới
		if expiredAt != nil && expiredAt.Before(time.Now()) {
			log.Printf("♻️ Document %s exists but is EXPIRED. Deleting and re-processing.", existingDocID)
			_, _ = config.DB.Exec(config.Ctx, "DELETE FROM documents WHERE id = $1", existingDocID)
			existingDocID = "" // Reset để luồng đi xuống phần tạo mới
		} else {
			// File ĐANG CÒN HẠN -> Link user vào bản ghi hiện có
			_, err = config.DB.Exec(config.Ctx, `
				INSERT INTO document_references (user_id, document_id, is_owner, pinned)
				VALUES ($1, $2, FALSE, FALSE)
				ON CONFLICT (user_id, document_id) DO NOTHING`, 
				userID, existingDocID,
			)

			c.JSON(200, gin.H{
				"success": true,
				"data": gin.H{
					"document_id": existingDocID,
					"status":      currentStatus,
					"message":     "Tài liệu đã tồn tại, được thêm vào thư viện của bạn.",
					"is_duplicate": true,
				},
			})
			return
		}
	}

	// 2. Nếu là file mới hoàn toàn -> Tiếp tục quy trình bình thường
	cloudinaryURL := c.PostForm("cloudinary_url")
	filename := c.PostForm("filename")
	if filename == "" {
		filename = fileHeader.Filename
	}

	// Tạo thư mục tạm nếu chưa có
	uploadDir := "./tmp/uploads"
	os.MkdirAll(uploadDir, os.ModePerm)
	docID := uuid.New().String()
	localPath := fmt.Sprintf("%s/%s-%s", uploadDir, docID, filename)

	// Lưu file local
	if err := c.SaveUploadedFile(fileHeader, localPath); err != nil {
		fmt.Printf("❌ Lỗi SaveUploadedFile: %v\n", err)
		c.JSON(500, gin.H{"success": false, "message": "Lỗi khi lưu file tạm"})
		return
	}

	// 4. Lưu bản ghi pending vào DB (với file_hash)
	_, err = config.DB.Exec(
		config.Ctx,
		`INSERT INTO documents (id, user_id, title, cloudinary_url, status, creator_persona, expired_at, file_hash) 
		 VALUES ($1, $2, $3, $4, 'queued', $5, NOW() + INTERVAL '24 hours', $6)`,
		docID, userID, filename, cloudinaryURL, userPersona, fileHash,
	)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể tạo tài liệu trong DB (Có thể file đang được xử lý bởi người khác)"})
		return
	}

	// Tạo reference cho chính uploader
	_, _ = config.DB.Exec(config.Ctx, `
		INSERT INTO document_references (user_id, document_id, is_owner, pinned)
		VALUES ($1, $2, TRUE, FALSE) ON CONFLICT DO NOTHING`, 
		userID, docID,
	)

	// 5. Đẩy job vào Redis Queue
	job := UploadJob{
		DocID:     docID,
		LocalPath: localPath,
		UserID:    userID,
	}
	payload, _ := json.Marshal(job)

	err = config.RedisClient.LPush(config.Ctx, config.Env.RedisQueueName, payload).Err()
	if err != nil {
		config.DB.Exec(config.Ctx, `UPDATE documents SET status='error' WHERE id=$1`, docID)
		c.JSON(500, gin.H{"success": false, "message": "Lỗi hệ thống hàng đợi"})
		return
	}

	c.JSON(202, gin.H{
		"success": true,
		"data": gin.H{
			"document_id": docID,
			"status":      "queued",
			"message":     "Tải lên thành công, đang bắt đầu xử lý...",
		},
	})
}

// Lấy tiến độ xử lý qua SSE stream
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

	key := "doc_progress:" + docID

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
			// Đọc từ Redis
			val, err := config.RedisClient.Get(config.Ctx, key).Result()
			if err != nil {
				// Nếu chưa có trong redis thì gửi trạng thái mặc định
				fmt.Fprintf(c.Writer, "data: {\"status\":\"pending\",\"progress\":0}\n\n")
				flusher.Flush()
				continue
			}

			fmt.Fprintf(c.Writer, "data: %s\n\n", val)
			flusher.Flush()

			// Nếu đã xong hoặc lỗi thì dừng stream
			var res map[string]interface{}
			json.Unmarshal([]byte(val), &res)
			if res["status"] == "ready" || res["status"] == "error" {
				return
			}
		}
	}
}
