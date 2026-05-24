package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type AppConfig struct {
	Port                   string
	DatabaseURL            string
	RedisURL               string
	JWTSecret              string
	JWTRefreshSecret       string
	CORSOrigins            []string
	GeminiChatKeys         []string
	GeminiEmbedKeys        []string
	GroqKeys               []string
	CerebrasKeys           []string
	MistralKeys            []string
	OpenRouterKeys         []string
	HuggingFaceKeys        []string
	SMTPHost               string
	SMTPPort               string
	SMTPUser               string
	SMTPPass               string
	SMTPFrom               string
	GoogleClientID         string
	RedisQueueName         string
	NineRouterKeys         []string
	NineRouterBaseURL      string
	NineRouterModel        string
	NineRouterChatKeys     []string
	NineRouterChatModel    string
	FPTAITTSKeys           []string
	WebSearchEnabled       bool
	WebSearchProviderOrder []string
	WebSearchDefaultGL     string
	WebSearchDefaultHL     string
	WebSearchMaxResults    int
	SerperKeys             []string
	TavilyKeys             []string
	BraveSearchKeys        []string
	ExaKeys                []string
	GoogleCSEAPIKey        string
	GoogleCSECX            string
	SerperMonthlySafe      int
	TavilyMonthlySafe      int
	BraveSearchMonthlySafe int
	ExaMonthlySafe         int
	GoogleCSEMonthlySafe   int
	GoogleCSEDailySafe     int
}

var Env AppConfig

func splitCSV(raw string) []string {
	var values []string
	for _, v := range strings.Split(raw, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			values = append(values, v)
		}
	}
	return values
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

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

	nineRouterKeysRaw := os.Getenv("NINEROUTER_API_KEYS")
	var nineRouterKeys []string
	if nineRouterKeysRaw != "" {
		parts := strings.Split(nineRouterKeysRaw, ",")
		for _, v := range parts {
			nineRouterKeys = append(nineRouterKeys, strings.TrimSpace(v))
		}
	}

	nineRouterChatKeysRaw := os.Getenv("NINEROUTER_CHAT_KEYS")
	var nineRouterChatKeys []string
	if nineRouterChatKeysRaw != "" {
		parts := strings.Split(nineRouterChatKeysRaw, ",")
		for _, v := range parts {
			nineRouterChatKeys = append(nineRouterChatKeys, strings.TrimSpace(v))
		}
	} else {
		// Fallback dùng chung key nếu không có chat key riêng
		nineRouterChatKeys = nineRouterKeys
	}

	// CORS origins — đọc từ env, fallback về các domain mặc định
	defaultOrigins := []string{
		"http://localhost:3000",
		"https://mindex.io.vn",
		"https://mindex-frontend.haidepzai92006.workers.dev",
	}

	fptAiKeysRaw := os.Getenv("FPT_AI_TTS_KEYS")
	var fptAiKeys []string
	if fptAiKeysRaw != "" {
		parts := strings.Split(fptAiKeysRaw, ",")
		for _, v := range parts {
			fptAiKeys = append(fptAiKeys, strings.TrimSpace(v))
		}
	}
	var corsOrigins []string
	if raw := os.Getenv("CORS_ORIGINS"); raw != "" {
		for _, o := range strings.Split(raw, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				corsOrigins = append(corsOrigins, o)
			}
		}
	}
	if len(corsOrigins) == 0 {
		corsOrigins = defaultOrigins
	}

	webSearchProviderOrder := splitCSV(os.Getenv("WEB_SEARCH_PROVIDER_ORDER"))
	if len(webSearchProviderOrder) == 0 {
		webSearchProviderOrder = []string{"serper", "tavily", "exa", "google_cse", "ddg"}
	}

	webSearchDefaultGL := os.Getenv("WEB_SEARCH_DEFAULT_GL")
	if webSearchDefaultGL == "" {
		webSearchDefaultGL = "vn"
	}
	webSearchDefaultHL := os.Getenv("WEB_SEARCH_DEFAULT_HL")
	if webSearchDefaultHL == "" {
		webSearchDefaultHL = "vi"
	}

	Env = AppConfig{
		Port:             port,
		DatabaseURL:      dbURL,
		RedisURL:         redisURL,
		JWTSecret:        os.Getenv("JWT_SECRET"),
		JWTRefreshSecret: os.Getenv("JWT_REFRESH_SECRET"),
		CORSOrigins:      corsOrigins,
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
		RedisQueueName: func() string {
			if q := os.Getenv("REDIS_QUEUE_NAME"); q != "" {
				return q
			}
			return "upload_queue"
		}(),
		NineRouterKeys:     nineRouterKeys,
		NineRouterBaseURL:  os.Getenv("NINEROUTER_BASE_URL"),
		NineRouterModel:    os.Getenv("NINEROUTER_MODEL"),
		NineRouterChatKeys: nineRouterChatKeys,
		NineRouterChatModel: func() string {
			if m := os.Getenv("NINEROUTER_CHAT_MODEL"); m != "" {
				return m
			}
			return "Mindex2" // Default
		}(),
		FPTAITTSKeys:           fptAiKeys,
		WebSearchEnabled:       strings.EqualFold(os.Getenv("WEB_SEARCH_ENABLED"), "true"),
		WebSearchProviderOrder: webSearchProviderOrder,
		WebSearchDefaultGL:     webSearchDefaultGL,
		WebSearchDefaultHL:     webSearchDefaultHL,
		WebSearchMaxResults:    envInt("WEB_SEARCH_MAX_RESULTS", 5),
		SerperKeys:             splitCSV(os.Getenv("SERPER_API_KEYS")),
		TavilyKeys:             splitCSV(os.Getenv("TAVILY_API_KEYS")),
		BraveSearchKeys:        splitCSV(os.Getenv("BRAVE_SEARCH_API_KEYS")),
		ExaKeys:                splitCSV(os.Getenv("EXA_API_KEYS")),
		GoogleCSEAPIKey:        strings.TrimSpace(os.Getenv("GOOGLE_CSE_API_KEY")),
		GoogleCSECX:            strings.TrimSpace(os.Getenv("GOOGLE_CSE_CX")),
		SerperMonthlySafe:      envInt("SERPER_MONTHLY_SAFE", 2200),
		TavilyMonthlySafe:      envInt("TAVILY_MONTHLY_SAFE", 880),
		BraveSearchMonthlySafe: envInt("BRAVE_SEARCH_MONTHLY_SAFE", 880),
		ExaMonthlySafe:         envInt("EXA_MONTHLY_SAFE", 880),
		GoogleCSEMonthlySafe:   envInt("GOOGLE_CSE_MONTHLY_SAFE", 2700),
		GoogleCSEDailySafe:     envInt("GOOGLE_CSE_DAILY_SAFE", 88),
	}
}
