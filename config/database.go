package config

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

var DB *pgxpool.Pool

func ConnectDB() {
	var err error

	// Sử dụng DatabaseURL đã được chọn từ config.go (Neon hoặc Local)
	config, err := pgxpool.ParseConfig(Env.DatabaseURL)
	if err != nil {
		log.Fatalf("Không thể parse cấu hình database: %v", err)
	}

	DB, err = pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Fatalf("Lỗi kết nối database: %v", err)
	}

	err = DB.Ping(context.Background())
	if err != nil {
		log.Fatalf("Không thể ping đến database: %v", err)
	}

	log.Println("✅ Đã kết nối thành công tới PostgreSQL Database")

	// Tự động migration: Phải dùng CONSTRAINT thay vì INDEX để ON CONFLICT hoạt động
	_, _ = DB.Exec(context.Background(), `
		DO $$ 
		BEGIN 
			-- Ràng buộc sesion_id cho chat
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'unique_session_id') THEN
				ALTER TABLE chat_histories ADD CONSTRAINT unique_session_id UNIQUE (session_id);
			END IF;

			-- Ràng buộc file_hash cho documents (SHA-256 UNIQUE)
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'unique_file_hash') THEN
				ALTER TABLE documents ADD CONSTRAINT unique_file_hash UNIQUE (file_hash);
			END IF;

			-- Ràng buộc (user_id, document_id) cho references
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'unique_user_doc') THEN
				ALTER TABLE document_references ADD CONSTRAINT unique_user_doc UNIQUE (user_id, document_id);
			END IF;

			-- Bổ sung các cột Gamification & Community cho documents nếu chưa có
			IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='documents' AND column_name='query_count') THEN
				ALTER TABLE documents ADD COLUMN query_count INTEGER NOT NULL DEFAULT 0;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='documents' AND column_name='upvote_count') THEN
				ALTER TABLE documents ADD COLUMN upvote_count INTEGER NOT NULL DEFAULT 0;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='documents' AND column_name='contributor_rewarded') THEN
				ALTER TABLE documents ADD COLUMN contributor_rewarded BOOLEAN NOT NULL DEFAULT FALSE;
			END IF;
			IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='documents' AND column_name='creator_persona') THEN
				ALTER TABLE documents ADD COLUMN creator_persona VARCHAR(20) DEFAULT 'student';
			END IF;
			IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='documents' AND column_name='expiration_notified') THEN
				ALTER TABLE documents ADD COLUMN expiration_notified BOOLEAN NOT NULL DEFAULT FALSE;
			END IF;

			-- Bổ sung contributor_id
			IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='documents' AND column_name='contributor_id') THEN
				ALTER TABLE documents ADD COLUMN contributor_id UUID REFERENCES users(id) ON DELETE SET NULL;
			END IF;

			-- Bổ sung cột prompt_no_context cho persona_prompts nếu chưa có
			IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='persona_prompts' AND column_name='prompt_no_context') THEN
				ALTER TABLE persona_prompts ADD COLUMN prompt_no_context TEXT;
			END IF;

			-- Bổ sung cột google_id cho users nếu chưa có
			IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='users' AND column_name='google_id') THEN
				ALTER TABLE users ADD COLUMN google_id VARCHAR(100);
				-- Đảm bảo google_id là duy nhất
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'unique_google_id') THEN
					ALTER TABLE users ADD CONSTRAINT unique_google_id UNIQUE (google_id);
				END IF;
			END IF;
			-- Bổ sung cột tier cho users nếu chưa có
			IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='users' AND column_name='tier') THEN
				ALTER TABLE users ADD COLUMN tier VARCHAR(20) DEFAULT 'FREE';
			END IF;
		END $$;
	`)

	// Migration: Bảng payments và system_settings
	_, _ = DB.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS system_settings (
			key   VARCHAR(50) PRIMARY KEY,
			value TEXT NOT NULL
		);
		-- Khởi tạo giá trị mặc định nếu chưa có
		INSERT INTO system_settings (key, value) VALUES ('PRO_PRICE', '5000') ON CONFLICT (key) DO NOTHING;
		INSERT INTO system_settings (key, value) VALUES ('ULTRA_PRICE', '10000') ON CONFLICT (key) DO NOTHING;

		CREATE TABLE IF NOT EXISTS payments (
			id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			order_code   BIGINT UNIQUE NOT NULL,
			amount       INTEGER NOT NULL,
			package_name VARCHAR(50) NOT NULL,
			status       VARCHAR(20) NOT NULL DEFAULT 'PENDING',
			created_at   TIMESTAMPTZ DEFAULT NOW(),
			updated_at   TIMESTAMPTZ DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_payments_user_id ON payments(user_id);
	`)
	log.Println("✅ Migration payments & system_settings hoàn tất")

	// Migration: Bảng notifications để lưu lịch sử thông báo realtime
	_, _ = DB.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS notifications (
			id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			type          VARCHAR(50) NOT NULL,
			title         TEXT NOT NULL,
			message       TEXT NOT NULL,
			data          JSONB,
			read_at       TIMESTAMPTZ,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_notifications_user_id ON notifications(user_id, created_at DESC);
	`)
	log.Println("✅ Migration notifications hoàn tất")

	// Migration: Bảng shared_links cho tính năng "Share as Template"
	_, _ = DB.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS shared_links (
			id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			document_id  UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			session_id   VARCHAR(64) NOT NULL,
			creator_id   UUID NOT NULL REFERENCES users(id),
			settings     JSONB NOT NULL DEFAULT '{"show_history": true, "allow_fork": true}',
			summary      TEXT,
			created_at   TIMESTAMPTZ DEFAULT NOW(),
			expired_at   TIMESTAMPTZ
		);
		CREATE INDEX IF NOT EXISTS idx_shared_links_doc ON shared_links(document_id);
		CREATE INDEX IF NOT EXISTS idx_shared_links_session ON shared_links(session_id);
	`)
	log.Println("✅ Migration shared_links hoàn tất")

	// Migration: pgvector & HNSW index để tìm kiếm ngữ nghĩa siêu tốc
	_, _ = DB.Exec(context.Background(), `
		CREATE EXTENSION IF NOT EXISTS vector;
		-- Index HNSW cho tìm kiếm Cosine Similarity (vector_cosine_ops)
		-- Giúp tìm kiếm vector hàng ngàn bản ghi trong vài millisecond
		CREATE INDEX IF NOT EXISTS idx_document_chunks_embedding_hnsw 
		ON document_chunks USING hnsw (embedding vector_cosine_ops);

		-- Bổ sung tìm kiếm văn bản tốc độ cao (Trigram Index) để tối ưu hóa ILIKE
		CREATE EXTENSION IF NOT EXISTS pg_trgm;
		CREATE INDEX IF NOT EXISTS trgm_idx_title ON documents USING gin (title gin_trgm_ops);
	`)
	log.Println("✅ Migration pgvector, HNSW & pg_trgm index hoàn tất")

	// Migration: Bảng document_votes để tránh upvote lặp
	_, _ = DB.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS document_votes (
			id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			created_at  TIMESTAMPTZ DEFAULT NOW(),
			UNIQUE(user_id, document_id)
		);
		CREATE INDEX IF NOT EXISTS idx_doc_votes_doc ON document_votes(document_id);
	`)
	log.Println("✅ Migration document_votes hoàn tất")
    
	// Migration: Bảng api_key_quotas để lưu trữ quota bền vững
	_, _ = DB.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS api_key_quotas (
			id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			api_key_id         TEXT NOT NULL,          -- Masked key
			provider           TEXT NOT NULL,
			rpd_used           BIGINT DEFAULT 0,
			monthly_token_used BIGINT DEFAULT 0,
			last_used          TIMESTAMPTZ DEFAULT NOW(),
			updated_at         TIMESTAMPTZ DEFAULT NOW(),
			UNIQUE(api_key_id, provider)
		);
	`)
	log.Println("✅ Migration api_key_quotas hoàn tất")

	// Migration: search_histories để lưu lịch sử tìm kiếm cộng đồng
	_, _ = DB.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS search_histories (
			id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			query       TEXT NOT NULL,
			created_at  TIMESTAMPTZ DEFAULT NOW(),
			UNIQUE(user_id, query)
		);
		CREATE INDEX IF NOT EXISTS idx_search_histories_user_id ON search_histories(user_id, created_at DESC);
	`)
	log.Println("✅ Migration search_histories hoàn tất")
}

func CloseDB() {
	if DB != nil {
		DB.Close()
		log.Println("✅ Đã ngắt kết nối Database an toàn")
	}
}
