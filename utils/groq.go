package utils

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type GroqRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// StreamGroqChat completion and forward Server-Sent Events to Gin Context Client
func StreamGroqChat(c *gin.Context, messages []ChatMessage) (string, error) {
	reqBody := GroqRequest{
		Model:    "llama-3.3-70b-versatile", 
		Messages: messages,
		Stream:   true,
	}

	jsonBytes, _ := json.Marshal(reqBody)
	key, alias := GroqPool.GetKey()
	if key == "" {
		return "", errors.New("không có Groq API Key trong pool")
	}

	req, _ := http.NewRequest("POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	log.Printf("🚀 Groq Stream: Khởi tạo với %s cho model %s", alias, reqBody.Model)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", errors.New("groq API failed with status " + resp.Status)
	}

	// Cập nhật Quota Status từ Token Headers
	UpdateKeyStatusFromHeaders("groq", alias, key, resp.Header)

	// NOTE: Headers are already set in ChatMessage Controller (controllers/chat.go)
	// Do NOT set headers again here to avoid breaking the stream FLUSH.

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return "", errors.New("streaming unsupported")
	}

	scanner := bufio.NewScanner(resp.Body)
	var fullAnswer string

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 6 || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := line[6:]
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err == nil && len(chunk.Choices) > 0 {
			content := chunk.Choices[0].Delta.Content
            // Chỉ gửi nếu có nội dung để tiết kiệm băng thông và buffer
            if content != "" {
                fullAnswer += content
                // Debug log: In ra terminal backend từng chữ AI đang nói
                fmt.Printf("%s", content)
                
                // Gửi token dưới dạng JSON an toàn
                tokenPayload, _ := json.Marshal(map[string]string{"token": content})
                fmt.Fprintf(c.Writer, "event: token\ndata: %s\n\n", string(tokenPayload))
                flusher.Flush()
            }
		}
	}

	return fullAnswer, nil
}

// StreamGroqChatNonStream for JSON responses (Summary/Extract)
func StreamGroqChatNonStream(messages []ChatMessage) (string, error) {
	reqBody := GroqRequest{
		Model:    "llama-3.3-70b-versatile",
		Messages: messages,
		Stream:   false,
	}

	jsonBytes, _ := json.Marshal(reqBody)
	key, alias := GroqPool.GetKey()
	if key == "" {
		return "", errors.New("không có Groq API Key trong pool")
	}

	req, _ := http.NewRequest("POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	log.Printf("🤖 Groq Non-Stream: Sử dụng %s cho model %s", alias, reqBody.Model)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", errors.New("groq API failed with status " + resp.Status)
	}

	// Cập nhật Quota Status từ Token Headers
	UpdateKeyStatusFromHeaders("groq", alias, key, resp.Header)

	var res struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	if len(res.Choices) > 0 {
		return res.Choices[0].Message.Content, nil
	}
	return "", nil
}

func CleanJSONString(s string) string {
	s = strings.TrimSpace(s)
	
	// Ưu tiên tìm cặp { } bao ngoài cùng
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	
	if start != -1 && end != -1 && end > start {
		return s[start : end+1]
	}
	
	// Fallback cho markdown
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
