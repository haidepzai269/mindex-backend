package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"mindex-backend/config"
)

type ProcessingClient struct {
	RedisClient *redis.Client
	BaseURL     string
	QueueName   string
	ResultPrefix string
}

type processingRequest struct {
	TaskID    string      `json:"task_id"`
	TaskType  string      `json:"task_type"`
	Payload   any `json:"payload"`
	CreatedAt string      `json:"created_at"`
}

type processingResult struct {
	TaskID     string          `json:"task_id"`
	Status     string          `json:"status"`
	Result     json.RawMessage `json:"result"`
	Error      *string         `json:"error"`
	DurationMs int             `json:"duration_ms"`
}

type syncResponse struct {
	Success    bool            `json:"success"`
	TaskType   string          `json:"task_type"`
	Result     json.RawMessage `json:"result"`
	DurationMs int             `json:"duration_ms"`
	Error      string          `json:"error"`
	Message    string          `json:"message"`
}

var Processing *ProcessingClient

func InitProcessingClient(redisClient *redis.Client, baseURL string) {
	Processing = &ProcessingClient{
		RedisClient:  redisClient,
		BaseURL:      baseURL,
		QueueName:    "mindex:processing:requests",
		ResultPrefix: "mindex:processing:result:",
	}
	log.Printf("Processing client initialized: %s", baseURL)
}

func (c *ProcessingClient) CallAsync(taskType string, payload any, timeout time.Duration) (json.RawMessage, error) {
	taskID := uuid.New().String()
	req := processingRequest{
		TaskID:    taskID,
		TaskType:  taskType,
		Payload:   payload,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal task request: %w", err)
	}

	if err := c.RedisClient.RPush(config.Ctx, c.QueueName, data).Err(); err != nil {
		return nil, fmt.Errorf("push task to queue: %w", err)
	}

	resultKey := c.ResultPrefix + taskID
	deadline := time.Now().Add(timeout)
	pollInterval := 500 * time.Millisecond

	for time.Now().Before(deadline) {
		raw, err := c.RedisClient.Get(config.Ctx, resultKey).Bytes()
		if err == redis.Nil {
			time.Sleep(pollInterval)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("poll result: %w", err)
		}

		c.RedisClient.Del(config.Ctx, resultKey)

		var result processingResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("parse result: %w", err)
		}

		if result.Status == "failed" {
			errMsg := "unknown error"
			if result.Error != nil {
				errMsg = *result.Error
			}
			return nil, fmt.Errorf("processing failed: %s", errMsg)
		}

		return result.Result, nil
	}

	return nil, fmt.Errorf("processing timeout after %s", timeout)
}

func (c *ProcessingClient) CallSync(taskType string, payload any, timeout time.Duration) (json.RawMessage, error) {
	body := map[string]any{
		"task_type": taskType,
		"payload":   payload,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal sync request: %w", err)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Post(c.BaseURL+"/api/v1/process", "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("sync request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("processing service returned %d: %s", resp.StatusCode, string(respBody))
	}

	var syncResp syncResponse
	if err := json.Unmarshal(respBody, &syncResp); err != nil {
		return nil, fmt.Errorf("parse sync response: %w", err)
	}

	if !syncResp.Success {
		return nil, fmt.Errorf("processing failed: %s", syncResp.Message)
	}

	return syncResp.Result, nil
}
