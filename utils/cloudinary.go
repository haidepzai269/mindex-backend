package utils

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type CloudinaryUploadResult struct {
	SecureURL string
	PublicID  string
}

// GenerateCloudinarySignature tạo signature cho client upload file trực tiếp
func GenerateCloudinarySignature() (string, int64, string, string) {
	timestamp := time.Now().Unix()
	apiSecret := os.Getenv("CLOUDINARY_API_SECRET")
	apiKey := os.Getenv("CLOUDINARY_API_KEY")
	cloudName := os.Getenv("CLOUDINARY_CLOUD_NAME")

	// Cloudinary yêu cầu các tham số phải được sắp xếp theo bảng chữ cái trước khi hash
	// Ở đây ta có folder=mindex_uploads và timestamp=X
	stringToSign := fmt.Sprintf("folder=mindex_uploads&timestamp=%d%s", timestamp, apiSecret)

	hash := sha1.Sum([]byte(stringToSign))
	signature := hex.EncodeToString(hash[:])

	// Trả về full info để NextJS dùng được luôn
	// Ép resource_type là raw để PDF/DOCX được nhận diện đúng
	uploadURL := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/raw/upload", cloudName)

	return signature, timestamp, apiKey, uploadURL
}

func UploadRawToCloudinary(filePath, publicID string) (*CloudinaryUploadResult, error) {
	cloudName := os.Getenv("CLOUDINARY_CLOUD_NAME")
	apiKey := os.Getenv("CLOUDINARY_API_KEY")
	apiSecret := os.Getenv("CLOUDINARY_API_SECRET")
	if cloudName == "" || apiKey == "" || apiSecret == "" {
		return nil, fmt.Errorf("cloudinary is not configured")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open upload file: %w", err)
	}
	defer file.Close()

	if publicID == "" {
		publicID = "mindex_uploads/" + strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	signature := signCloudinaryParams(map[string]string{
		"public_id": publicID,
		"timestamp": timestamp,
	}, apiSecret)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return nil, fmt.Errorf("create cloudinary form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, fmt.Errorf("copy upload file: %w", err)
	}
	_ = writer.WriteField("api_key", apiKey)
	_ = writer.WriteField("timestamp", timestamp)
	_ = writer.WriteField("public_id", publicID)
	_ = writer.WriteField("signature", signature)
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close cloudinary form: %w", err)
	}

	endpoint := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/raw/upload", cloudName)
	req, err := http.NewRequest(http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("create cloudinary request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudinary upload request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("cloudinary upload returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var parsed struct {
		SecureURL string `json:"secure_url"`
		PublicID  string `json:"public_id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse cloudinary upload response: %w", err)
	}
	if parsed.SecureURL == "" {
		return nil, fmt.Errorf("cloudinary upload response missing secure_url")
	}
	if parsed.PublicID == "" {
		parsed.PublicID = publicID
	}
	return &CloudinaryUploadResult{SecureURL: parsed.SecureURL, PublicID: parsed.PublicID}, nil
}

func DestroyRawFromCloudinary(publicID string) error {
	cloudName := os.Getenv("CLOUDINARY_CLOUD_NAME")
	apiKey := os.Getenv("CLOUDINARY_API_KEY")
	apiSecret := os.Getenv("CLOUDINARY_API_SECRET")
	if cloudName == "" || apiKey == "" || apiSecret == "" || publicID == "" {
		return nil
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	signature := signCloudinaryParams(map[string]string{
		"public_id": publicID,
		"timestamp": timestamp,
	}, apiSecret)

	form := url.Values{}
	form.Set("public_id", publicID)
	form.Set("timestamp", timestamp)
	form.Set("api_key", apiKey)
	form.Set("signature", signature)

	endpoint := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/raw/destroy", cloudName)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.PostForm(endpoint, form)
	if err != nil {
		return fmt.Errorf("cloudinary delete request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("cloudinary delete returned %d", resp.StatusCode)
	}
	return nil
}

func signCloudinaryParams(params map[string]string, apiSecret string) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, params[key]))
	}
	hash := sha1.Sum([]byte(strings.Join(parts, "&") + apiSecret))
	return hex.EncodeToString(hash[:])
}
