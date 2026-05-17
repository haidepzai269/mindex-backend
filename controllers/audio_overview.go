package controllers

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"mindex-backend/config"
	"mindex-backend/utils"

	"github.com/gin-gonic/gin"
)

type AudioOverview struct {
	ID        string      `json:"id"`
	DocID     string      `json:"doc_id"`
	UserID    string      `json:"user_id"`
	Script    interface{} `json:"script"`
	AudioURL  string      `json:"audio_url"`
	Status    string      `json:"status"`
	CreatedAt time.Time   `json:"created_at"`
}

type ScriptLine struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

var currentFptKeyIndex uint32 = 0

func getNextFptKey() string {
	keys := config.Env.FPTAITTSKeys
	if len(keys) == 0 {
		return ""
	}
	idx := atomic.AddUint32(&currentFptKeyIndex, 1)
	return keys[idx%uint32(len(keys))]
}

func GenerateAudioOverview(c *gin.Context) {
	docID := c.Param("doc_id")
	userID := c.GetString("user_id")

	// Check if exists
	var existing AudioOverview
	err := config.DB.QueryRow(config.Ctx, `
		SELECT id, doc_id, user_id, script, audio_url, status, created_at 
		FROM audio_overviews 
		WHERE doc_id = $1 AND user_id = $2 
		ORDER BY created_at DESC LIMIT 1
	`, docID, userID).Scan(&existing.ID, &existing.DocID, &existing.UserID, &existing.Script, &existing.AudioURL, &existing.Status, &existing.CreatedAt)

	if err == nil && existing.Status == "done" {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": existing})
		return
	}

	if err == nil && existing.Status == "pending" {
		c.JSON(http.StatusAccepted, gin.H{"success": true, "message": "Đang tạo Audio Overview..."})
		return
	}

	// Fetch top 30 chunks
	rows, err := config.DB.Query(config.Ctx, `
		SELECT COALESCE(retrieval_content, content)
		FROM document_chunks 
		WHERE document_id = $1 
		ORDER BY chunk_index ASC 
		LIMIT 30
	`, docID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to fetch document chunks"})
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
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Document is empty"})
		return
	}

	// Create pending record
	var recordID string
	err = config.DB.QueryRow(config.Ctx, `
		INSERT INTO audio_overviews (doc_id, user_id, status)
		VALUES ($1, $2, 'pending')
		RETURNING id
	`, docID, userID).Scan(&recordID)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Database error"})
		return
	}

	// Start generation in background
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Println("Recovered in GenerateAudioOverview:", r)
				config.DB.Exec(config.Ctx, "UPDATE audio_overviews SET status = 'failed' WHERE id = $1", recordID)
			}
		}()

		// Call AI Orchestrator (Groq → NineRouter → Gemini) to generate script
		prompt := "Bạn là biên tập viên làm podcast tóm tắt tài liệu. Dưới đây là nội dung tài liệu. Hãy viết một kịch bản trò chuyện (dialogue) tiếng Việt ngắn gọn, súc tích (khoảng 20-30 lượt thoại) giữa 2 người (A: Nữ, B: Nam) để tóm tắt các ý chính một cách tự nhiên, dễ hiểu. Trả về đúng định dạng JSON array: [{\"speaker\":\"A\",\"text\":\"...\"},{\"speaker\":\"B\",\"text\":\"...\"}]. Không thêm markdown hay text nào khác."

		messages := []utils.ChatMessage{
			{Role: "system", Content: prompt},
			{Role: "user", Content: combinedText},
		}

		scriptJSONStr, _, err := utils.AI.ChatNonStream(utils.ServiceAudio, messages)
		if err != nil {
			log.Println("Audio AI error:", err)
			config.DB.Exec(config.Ctx, "UPDATE audio_overviews SET status = 'failed' WHERE id = $1", recordID)
			return
		}

		// Clean JSON
		scriptJSONStr = strings.TrimSpace(scriptJSONStr)
		scriptJSONStr = strings.TrimPrefix(scriptJSONStr, "```json")
		scriptJSONStr = strings.TrimPrefix(scriptJSONStr, "```")
		scriptJSONStr = strings.TrimSuffix(scriptJSONStr, "```")
		scriptJSONStr = strings.TrimSpace(scriptJSONStr)

		var script []ScriptLine
		if err := json.Unmarshal([]byte(scriptJSONStr), &script); err != nil {
			log.Println("JSON parse error:", err)
			config.DB.Exec(config.Ctx, "UPDATE audio_overviews SET status = 'failed' WHERE id = $1", recordID)
			return
		}

		// Process TTS for each line
		var finalAudioBytes []byte

		for _, line := range script {
			audioBytes, err := generateTTSForLine(line)
			if err != nil {
				log.Println("TTS error:", err)
				config.DB.Exec(config.Ctx, "UPDATE audio_overviews SET status = 'failed' WHERE id = $1", recordID)
				return
			}
			finalAudioBytes = append(finalAudioBytes, audioBytes...)
		}

		// Check if audio bytes are valid
		if len(finalAudioBytes) == 0 {
			log.Println("Error: No audio bytes generated")
			config.DB.Exec(config.Ctx, "UPDATE audio_overviews SET status = 'failed' WHERE id = $1", recordID)
			return
		}

		// Basic MP3 validation (check for ID3 or frame header)
		if len(finalAudioBytes) < 4 {
			log.Println("Error: Audio file too small to be valid MP3")
			config.DB.Exec(config.Ctx, "UPDATE audio_overviews SET status = 'failed' WHERE id = $1", recordID)
			return
		}

		log.Printf("✅ Generated %d audio bytes for %d lines", len(finalAudioBytes), len(script))

		// Upload to Cloudinary
		tmpFile := filepath.Join(os.TempDir(), recordID+".mp3")
		os.WriteFile(tmpFile, finalAudioBytes, 0644)
		defer os.Remove(tmpFile)

		cloudinaryURL, err := uploadAudioToCloudinary(tmpFile, recordID)
		if err != nil {
			log.Println("Cloudinary error:", err)
			config.DB.Exec(config.Ctx, "UPDATE audio_overviews SET status = 'failed' WHERE id = $1", recordID)
			return
		}

		if cloudinaryURL == "" {
			log.Println("Error: Cloudinary returned empty URL")
			config.DB.Exec(config.Ctx, "UPDATE audio_overviews SET status = 'failed' WHERE id = $1", recordID)
			return
		}

		log.Printf("✅ Cloudinary URL: %s", cloudinaryURL)

		// Update DB
		scriptBytes, _ := json.Marshal(script)
		_, err = config.DB.Exec(config.Ctx, `
			UPDATE audio_overviews 
			SET audio_url = $1, script = $2, status = 'done' 
			WHERE id = $3
		`, cloudinaryURL, scriptBytes, recordID)

		if err != nil {
			log.Println("Update DB error:", err)
		}
	}()

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Bắt đầu tạo Audio Overview"})
}

func GetAudioOverview(c *gin.Context) {
	docID := c.Param("doc_id")
	userID := c.GetString("user_id")

	var result AudioOverview
	var scriptBytes []byte

	err := config.DB.QueryRow(config.Ctx, `
		SELECT id, doc_id, user_id, 
		       COALESCE(script, '{}'::jsonb), 
		       COALESCE(audio_url, ''), 
		       status, created_at 
		FROM audio_overviews 
		WHERE doc_id = $1 AND user_id = $2 
		ORDER BY created_at DESC LIMIT 1
	`, docID, userID).Scan(&result.ID, &result.DocID, &result.UserID, &scriptBytes, &result.AudioURL, &result.Status, &result.CreatedAt)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Not found"})
		return
	}

	if len(scriptBytes) > 0 {
		json.Unmarshal(scriptBytes, &result.Script)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// Helper: TTS Generation
func generateTTSForLine(line ScriptLine) ([]byte, error) {
	// Try Edge TTS first
	edgeVoice := "vi-VN-HoaiMyNeural"
	if line.Speaker == "B" {
		edgeVoice = "vi-VN-NamMinhNeural"
	}

	tmpEdgeFile := filepath.Join(os.TempDir(), "edge_tmp_"+fmt.Sprintf("%d", time.Now().UnixNano())+".mp3")
	cmd := exec.Command("edge-tts", "--voice", edgeVoice, "--text", line.Text, "--write-media", tmpEdgeFile)
	err := cmd.Run()

	if err == nil {
		data, err := os.ReadFile(tmpEdgeFile)
		os.Remove(tmpEdgeFile)
		if err == nil && len(data) > 0 {
			return data, nil
		}
	}

	// Fallback to FPT AI
	log.Println("Edge-TTS failed or unavailable, falling back to FPT AI TTS...")
	fptKey := getNextFptKey()
	if fptKey == "" {
		return nil, fmt.Errorf("no FPT AI keys available")
	}

	fptVoice := "banmai"
	if line.Speaker == "B" {
		fptVoice = "minhquang"
	}

	req, _ := http.NewRequest("POST", "https://api.fpt.ai/hmi/tts/v5", bytes.NewBuffer([]byte(line.Text)))
	req.Header.Set("api-key", fptKey)
	req.Header.Set("voice", fptVoice)
	req.Header.Set("speed", "0")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("FPT AI returned status: %d", resp.StatusCode)
	}

	// FPT AI returns a JSON with async URL, wait... wait, FPT TTS v5 returns async URL!
	var fptRes struct {
		Async string `json:"async"`
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&fptRes)

	if fptRes.Async == "" {
		return nil, fmt.Errorf("FPT AI error: %s", fptRes.Error)
	}

	// Poll the async URL
	for i := 0; i < 20; i++ {
		time.Sleep(2 * time.Second)
		pollReq, _ := http.NewRequest("GET", fptRes.Async, nil)
		pollReq.Header.Set("api-key", fptKey)
		pollResp, err := client.Do(pollReq)
		if err == nil && pollResp.StatusCode == 200 {
			bodyBytes, _ := io.ReadAll(pollResp.Body)
			pollResp.Body.Close()
			if len(bodyBytes) > 100 { // Assuming a valid MP3 is > 100 bytes
				return bodyBytes, nil
			}
		}
		if pollResp != nil {
			pollResp.Body.Close()
		}
	}

	return nil, fmt.Errorf("timeout waiting for FPT AI")
}

// Helper: Cloudinary Upload for Audio
func uploadAudioToCloudinary(filePath, publicID string) (string, error) {
	// Re-using cloudinary keys from config
	cloudName := os.Getenv("CLOUDINARY_CLOUD_NAME")
	apiKey := os.Getenv("CLOUDINARY_API_KEY")
	apiSecret := os.Getenv("CLOUDINARY_API_SECRET")

	return uploadToCloudinaryViaRest(filePath, publicID, cloudName, apiKey, apiSecret)
}

func uploadToCloudinaryViaRest(filePath, publicID, cloudName, apiKey, apiSecret string) (string, error) {
	url := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/upload", cloudName)

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	stringToSign := fmt.Sprintf("folder=audio_overviews&public_id=%s&timestamp=%s%s", publicID, timestamp, apiSecret)

	hash := sha1.Sum([]byte(stringToSign))
	signature := hex.EncodeToString(hash[:])

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	writer.WriteField("api_key", apiKey)
	writer.WriteField("timestamp", timestamp)
	writer.WriteField("signature", signature)
	writer.WriteField("folder", "audio_overviews")
	writer.WriteField("public_id", publicID)

	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", err
	}
	io.Copy(part, file)
	writer.Close()

	req, err := http.NewRequest("POST", url, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("cloudinary upload failed: %d - %s", resp.StatusCode, string(respBody))
	}

	var res struct {
		SecureURL string `json:"secure_url"`
	}
	json.NewDecoder(resp.Body).Decode(&res)

	return res.SecureURL, nil
}
