package controllers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mindex-backend/config"
	"mindex-backend/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Slide struct {
	Title           string   `json:"title"`
	Bullets         []string `json:"bullets"`
	Narration       string   `json:"narration"`
	Layout          string   `json:"layout"`
	Theme           string   `json:"theme"`
	AudioURL        string   `json:"audio_url"`
	DurationSeconds float64  `json:"duration_seconds"`
}

type Presentation struct {
	ID        string      `json:"id"`
	DocID     string      `json:"doc_id"`
	UserID    string      `json:"user_id"`
	Slides    interface{} `json:"slides_data"`
	Status    string      `json:"status"`
	CreatedAt time.Time   `json:"created_at"`
}

func GeneratePresentation(c *gin.Context) {
	docID := c.Param("doc_id")
	userID := c.GetString("user_id")

	// 1. Kiểm tra xem đã tồn tại slide cho tài liệu này chưa
	var existing Presentation
	var slidesBytes []byte
	err := config.DB.QueryRow(config.Ctx, `
		SELECT id, doc_id, user_id, COALESCE(slides_data, '{}'::jsonb), status, created_at
		FROM study_presentations
		WHERE doc_id = $1 AND user_id = $2
		ORDER BY created_at DESC LIMIT 1
	`, docID, userID).Scan(&existing.ID, &existing.DocID, &existing.UserID, &slidesBytes, &existing.Status, &existing.CreatedAt)

	// Nếu đã xong, trả về kết quả ngay (cache hit)
	if err == nil && existing.Status == "done" {
		if len(slidesBytes) > 0 {
			json.Unmarshal(slidesBytes, &existing.Slides)
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "data": existing})
		return
	}

	// Nếu đang sinh, báo cho client chờ
	if err == nil && existing.Status == "pending" {
		c.JSON(http.StatusAccepted, gin.H{"success": true, "status": "pending", "message": "Đang tạo Slide & Video thuyết trình..."})
		return
	}

	// 2. Lấy nội dung text từ tài liệu (top 30 chunks)
	rows, err := config.DB.Query(config.Ctx, `
		SELECT COALESCE(retrieval_content, content)
		FROM document_chunks
		WHERE document_id = $1
		ORDER BY chunk_index ASC
		LIMIT 30
	`, docID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Không thể lấy nội dung tài liệu"})
		return
	}
	defer rows.Close()

	var combinedText string
	for rows.Next() {
		var content string
		rows.Scan(&content)
		combinedText += content + "\n\n"
	}

	if combinedText == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Tài liệu trống, không thể tạo slide"})
		return
	}

	var docTitle string
	config.DB.QueryRow(config.Ctx, `SELECT title FROM documents WHERE id = $1`, docID).Scan(&docTitle)

	// 3. Tạo bản ghi trạng thái pending trong DB
	recordID := uuid.New().String()
	_, err = config.DB.Exec(config.Ctx, `
		INSERT INTO study_presentations (id, doc_id, user_id, status)
		VALUES ($1, $2, $3, 'pending')
	`, recordID, docID, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Lỗi lưu cơ sở dữ liệu"})
		return
	}

	// 4. Khởi chạy Goroutine xử lý nền
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Println("Recovered in GeneratePresentation background worker:", r)
				config.DB.Exec(config.Ctx, "UPDATE study_presentations SET status = 'failed' WHERE id = $1", recordID)
			}
		}()

		// Build prompt cho Gemini sinh slide JSON
		promptSys := `Bạn là chuyên gia thiết kế bài thuyết trình chuyên nghiệp. Hãy đọc nội dung tài liệu dưới đây và chuyển đổi nó thành một bộ Slide thuyết trình đẹp mắt, súc tích (khoảng 5 đến tối đa 8 slide).

Yêu cầu định dạng JSON trả về:
Bạn PHẢI trả về ĐÚNG MỘT JSON array hợp lệ (KHÔNG có markdown code block, KHÔNG bọc trong ` + "```" + `json) chứa danh sách các slide. Mỗi slide là một đối tượng có cấu trúc:
[
  {
    "title": "Tiêu đề của slide ngắn gọn, súc tích",
    "bullets": [
      "Ý chính thứ nhất cực kỳ cô đọng (dưới 15 từ)",
      "Ý chính thứ hai",
      "Ý chính thứ ba"
    ],
    "narration": "Kịch bản thuyết minh chi tiết cho slide này bằng tiếng Việt tự nhiên, trôi chảy như người thật thuyết trình (khoảng 30-50 từ).",
    "layout": "Chọn một trong các giá trị sau: 'bullets' (danh sách ý chính), 'split' (chia đôi cột trái-phải), 'quote' (trích dẫn nổi bật), 'highlight' (một ý chính cực to)",
    "theme": "Chọn một trong các giá trị theme gradient hiện đại: 'gradient-purple' (tím-xanh), 'gradient-ocean' (xanh biển-teal), 'gradient-sunset' (cam-hồng), 'dark' (xám tối-neon)"
  }
]

Yêu cầu nội dung:
- Slide 1 luôn là Slide mở đầu giới thiệu chung.
- Slide cuối là kết luận/tóm tắt lại.
- Các ý trong bullets phải ngắn gọn, tập trung khái niệm chính để trình diễn trực quan.
- Phần narration là kịch bản nói để hệ thống đọc tự động, cần viết mạch lạc và dễ nghe.`

		messages := []utils.ChatMessage{
			{Role: "system", Content: promptSys},
			{Role: "user", Content: fmt.Sprintf("Tài liệu: %s\n\nNội dung:\n%s", docTitle, combinedText)},
		}

		// Gọi AI để tạo slides
		rawJSON, _, err := utils.AI.ChatNonStream(utils.ServiceChat, messages)
		if err != nil {
			log.Println("Presentation AI error:", err)
			config.DB.Exec(config.Ctx, "UPDATE study_presentations SET status = 'failed' WHERE id = $1", recordID)
			return
		}

		// Làm sạch JSON nhận được
		rawJSON = strings.TrimSpace(rawJSON)
		rawJSON = strings.TrimPrefix(rawJSON, "```json")
		rawJSON = strings.TrimPrefix(rawJSON, "```")
		rawJSON = strings.TrimSuffix(rawJSON, "```")
		rawJSON = strings.TrimSpace(rawJSON)

		var slides []Slide
		if err := json.Unmarshal([]byte(rawJSON), &slides); err != nil {
			log.Printf("JSON parse error for presentation: %v. Raw: %s", err, rawJSON)
			config.DB.Exec(config.Ctx, "UPDATE study_presentations SET status = 'failed' WHERE id = $1", recordID)
			return
		}

		log.Printf("✅ Đang xử lý TTS cho %d slides của doc %s", len(slides), docID)

		// 5. Duyệt qua từng slide để tạo âm thanh thuyết minh (TTS)
		for idx := range slides {
			slide := &slides[idx]
			if slide.Narration == "" {
				// Nếu slide không có kịch bản nói, ước lượng thời lượng mặc định
				slide.DurationSeconds = 5.0
				continue
			}

			// Gọi Edge-TTS (hoặc FPT-TTS) thông qua helper của Audio Overview
			// Chúng ta sử dụng giọng đọc A (nữ HoaiMy) mặc định
			audioBytes, err := generateTTSForLine(ScriptLine{Speaker: "A", Text: slide.Narration})
			if err != nil {
				log.Printf("TTS error on slide %d: %v", idx, err)
				slide.DurationSeconds = float64(len(strings.Fields(slide.Narration))) / 2.5 // Tạm tính theo số từ
				if slide.DurationSeconds < 4.0 {
					slide.DurationSeconds = 4.0
				}
				continue
			}

			// Ước tính thời lượng âm thanh dựa trên số từ (khoảng 2.5 từ mỗi giây)
			wordCount := len(strings.Fields(slide.Narration))
			estimatedDuration := float64(wordCount) / 2.3 // 2.3 từ mỗi giây cho tiếng Việt tự nhiên
			if estimatedDuration < 4.0 {
				estimatedDuration = 4.0
			}
			slide.DurationSeconds = estimatedDuration

			// Ghi file tạm để upload lên Cloudinary
			slidePublicID := fmt.Sprintf("presentation_%s_slide_%d", recordID, idx)
			tmpFile := filepath.Join(os.TempDir(), slidePublicID+".mp3")
			err = os.WriteFile(tmpFile, audioBytes, 0644)
			if err != nil {
				log.Printf("Write temp file error on slide %d: %v", idx, err)
				continue
			}

			// Upload lên Cloudinary
			cloudinaryURL, err := uploadAudioToCloudinary(tmpFile, slidePublicID)
			os.Remove(tmpFile) // Xóa file tạm ngay

			if err != nil {
				log.Printf("Cloudinary upload error on slide %d: %v", idx, err)
				continue
			}

			slide.AudioURL = cloudinaryURL
			log.Printf("Slide %d: Audio URL generated -> %s (estimated duration: %.2f)", idx, cloudinaryURL, estimatedDuration)
		}

		// 6. Cập nhật kết quả vào database
		slidesJSONBytes, _ := json.Marshal(slides)
		_, err = config.DB.Exec(config.Ctx, `
			UPDATE study_presentations
			SET slides_data = $1, status = 'done'
			WHERE id = $2
		`, slidesJSONBytes, recordID)

		if err != nil {
			log.Println("Update presentation DB error:", err)
		} else {
			log.Printf("✅ Sinh thành công bộ slide thuyết trình cho doc %s (ID record: %s)", docID, recordID)
		}
	}()

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"status":  "pending",
		"message": "Bắt đầu tạo Slide & Video thuyết trình AI...",
	})
}

func GetPresentation(c *gin.Context) {
	docID := c.Param("doc_id")
	userID := c.GetString("user_id")

	var result Presentation
	var slidesBytes []byte

	err := config.DB.QueryRow(config.Ctx, `
		SELECT id, doc_id, user_id,
		       COALESCE(slides_data, '{}'::jsonb),
		       status, created_at
		FROM study_presentations
		WHERE doc_id = $1 AND user_id = $2
		ORDER BY created_at DESC LIMIT 1
	`, docID, userID).Scan(&result.ID, &result.DocID, &result.UserID, &slidesBytes, &result.Status, &result.CreatedAt)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Chưa có Slide thuyết trình cho tài liệu này"})
		return
	}

	if len(slidesBytes) > 0 {
		json.Unmarshal(slidesBytes, &result.Slides)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}
