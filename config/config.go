package config

import (
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type AppConfig struct {
	Port             string
	DatabaseURL      string
	RedisURL         string
	JWTSecret        string
	JWTRefreshSecret string
	GeminiChatKeys   []string
	GeminiEmbedKeys  []string
	GroqKeys         []string
	CerebrasKeys     []string
	MistralKeys      []string
	OpenRouterKeys   []string
	HuggingFaceKeys  []string
	SMTPHost         string
	SMTPPort         string
	SMTPUser         string
	SMTPPass         string
	SMTPFrom         string
	GoogleClientID   string
}

var Env AppConfig

func LoadConfig() {
	err := godotenv.Load()
	if err != nil {
		log.Println("Cảnh báo: Không tìm thấy file .env, sẽ dùng environment variables của hệ thống.")
	}

	// Logic chọn Database: Cloud (Neon) hay Local
	useCloudDB := os.Getenv("USE_CLOUD_DB")
	dbURL := os.Getenv("DATABASE_URL_LOCAL")
	if useCloudDB == "true" {
		dbURL = os.Getenv("DATABASE_URL_CLOUD")
		log.Println("Đang cấu hình sử dụng Cloud Database (Neon)...")
	} else {
		log.Println("Đang cấu hình sử dụng Local Database...")
	}

	// Logic chọn Redis: Cloud (Upstash) hay Local
	useCloudRedis := os.Getenv("USE_CLOUD_REDIS")
	redisURL := os.Getenv("REDIS_URL_LOCAL")
	if useCloudRedis == "true" {
		redisURL = os.Getenv("REDIS_URL_CLOUD")
		log.Println("Đang cấu hình sử dụng Cloud Redis (Upstash)...")
	} else {
		log.Println("Đang cấu hình sử dụng Local Redis...")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	geminiChatKeysRaw := os.Getenv("GEMINI_CHAT_KEYS")
	var geminiChatKeys []string
	if geminiChatKeysRaw != "" {
		geminiChatKeys = strings.Split(geminiChatKeysRaw, ",")
	}

	geminiEmbedKeysRaw := os.Getenv("GEMINI_EMBED_KEYS")
	var geminiEmbedKeys []string
	if geminiEmbedKeysRaw != "" {
		geminiEmbedKeys = strings.Split(geminiEmbedKeysRaw, ",")
	}

	groqKeysRaw := os.Getenv("GROQ_API_KEYS")
	var groqKeys []string
	if groqKeysRaw != "" {
		groqKeys = strings.Split(groqKeysRaw, ",")
	}

	hfKeysRaw := os.Getenv("HUGGINGFACE_API_KEYS")
	var hfKeys []string
	if hfKeysRaw != "" {
		hfKeys = strings.Split(hfKeysRaw, ",")
	}

	cerebrasKeysRaw := os.Getenv("CEREBRAS_API_KEYS")
	var cerebrasKeys []string
	if cerebrasKeysRaw != "" {
		cerebrasKeys = strings.Split(cerebrasKeysRaw, ",")
	}

	mistralKeysRaw := os.Getenv("MISTRAL_API_KEYS")
	var mistralKeys []string
	if mistralKeysRaw != "" {
		mistralKeys = strings.Split(mistralKeysRaw, ",")
	}

	openRouterKeysRaw := os.Getenv("OPENROUTER_API_KEYS")
	var openRouterKeys []string
	if openRouterKeysRaw != "" {
		openRouterKeys = strings.Split(openRouterKeysRaw, ",")
	}

	Env = AppConfig{
		Port:             port,
		DatabaseURL:      dbURL,
		RedisURL:         redisURL,
		JWTSecret:        os.Getenv("JWT_SECRET"),
		JWTRefreshSecret: os.Getenv("JWT_REFRESH_SECRET"),
		GeminiChatKeys:   geminiChatKeys,
		GeminiEmbedKeys:  geminiEmbedKeys,
		GroqKeys:         groqKeys,
		CerebrasKeys:     cerebrasKeys,
		MistralKeys:      mistralKeys,
		OpenRouterKeys:   openRouterKeys,
		HuggingFaceKeys:  hfKeys,
		SMTPHost:         os.Getenv("SMTP_HOST"),
		SMTPPort:         os.Getenv("SMTP_PORT"),
		SMTPUser:         os.Getenv("SMTP_USER"),
		SMTPPass:         os.Getenv("SMTP_PASS"),
		SMTPFrom:         os.Getenv("SMTP_FROM_EMAIL"),
		GoogleClientID:   os.Getenv("GOOGLE_CLIENT_ID"),
	}
}
