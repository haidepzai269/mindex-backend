package utils

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

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
