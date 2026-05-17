package startup

import (
	"log"
	"mindex-backend/config"
)

type startupMigration struct {
	name string
	sql  string
}

// RunMigrations applies idempotent startup DDL in one place.
// Versioned SQL files remain the source for manual/provisioning migrations.
func RunMigrations() {
	ok, fail := 0, 0
	for _, migration := range startupMigrations() {
		if _, err := config.DB.Exec(config.Ctx, migration.sql); err != nil {
			log.Printf("[Startup Migration] %s failed: %v", migration.name, err)
			fail++
			continue
		}
		ok++
	}

	if fail > 0 {
		log.Fatalf("[Startup Migrations] %d OK, %d failed", ok, fail)
	}
	log.Printf("[Startup Migrations] %d OK, %d failed", ok, fail)
}

func startupMigrations() []startupMigration {
	return []startupMigration{
		{"core compatibility constraints and columns", `
			DO $$
			BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'unique_session_id') THEN
					ALTER TABLE chat_histories ADD CONSTRAINT unique_session_id UNIQUE (session_id);
				END IF;

				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'unique_file_hash') THEN
					ALTER TABLE documents ADD CONSTRAINT unique_file_hash UNIQUE (file_hash);
				END IF;

				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'unique_user_doc') THEN
					ALTER TABLE document_references ADD CONSTRAINT unique_user_doc UNIQUE (user_id, document_id);
				END IF;

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
				IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='documents' AND column_name='shared_at') THEN
					ALTER TABLE documents ADD COLUMN shared_at TIMESTAMPTZ;
				END IF;
				IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='documents' AND column_name='contributor_id') THEN
					ALTER TABLE documents ADD COLUMN contributor_id UUID REFERENCES users(id) ON DELETE SET NULL;
				END IF;
				IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='persona_prompts' AND column_name='prompt_no_context') THEN
					ALTER TABLE persona_prompts ADD COLUMN prompt_no_context TEXT;
				END IF;
				IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='users' AND column_name='google_id') THEN
					ALTER TABLE users ADD COLUMN google_id VARCHAR(100);
				END IF;
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'unique_google_id') THEN
					ALTER TABLE users ADD CONSTRAINT unique_google_id UNIQUE (google_id);
				END IF;
				IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='users' AND column_name='tier') THEN
					ALTER TABLE users ADD COLUMN tier VARCHAR(20) DEFAULT 'FREE';
				END IF;
			END $$;
		`},
		{"system_settings and payments", `
			CREATE TABLE IF NOT EXISTS system_settings (
				key VARCHAR(50) PRIMARY KEY,
				value TEXT NOT NULL
			);
			INSERT INTO system_settings (key, value) VALUES ('PRO_PRICE', '5000') ON CONFLICT (key) DO NOTHING;
			INSERT INTO system_settings (key, value) VALUES ('ULTRA_PRICE', '10000') ON CONFLICT (key) DO NOTHING;

			CREATE TABLE IF NOT EXISTS payments (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				order_code BIGINT UNIQUE NOT NULL,
				amount INTEGER NOT NULL,
				package_name VARCHAR(50) NOT NULL,
				status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
				created_at TIMESTAMPTZ DEFAULT NOW(),
				updated_at TIMESTAMPTZ DEFAULT NOW()
			);
			CREATE INDEX IF NOT EXISTS idx_payments_user_id ON payments(user_id);
		`},
		{"notifications", `
			CREATE TABLE IF NOT EXISTS notifications (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				type VARCHAR(50) NOT NULL,
				title TEXT NOT NULL,
				message TEXT NOT NULL,
				data JSONB,
				read_at TIMESTAMPTZ,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);
			CREATE INDEX IF NOT EXISTS idx_notifications_user_id ON notifications(user_id, created_at DESC);
		`},
		{"shared_links", `
			CREATE TABLE IF NOT EXISTS shared_links (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
				session_id VARCHAR(64) NOT NULL,
				creator_id UUID NOT NULL REFERENCES users(id),
				settings JSONB NOT NULL DEFAULT '{"show_history": true, "allow_fork": true}',
				summary TEXT,
				created_at TIMESTAMPTZ DEFAULT NOW(),
				expired_at TIMESTAMPTZ
			);
			CREATE INDEX IF NOT EXISTS idx_shared_links_doc ON shared_links(document_id);
			CREATE INDEX IF NOT EXISTS idx_shared_links_session ON shared_links(session_id);
		`},
		{"vector and trigram indexes", `
			CREATE EXTENSION IF NOT EXISTS vector;
			CREATE EXTENSION IF NOT EXISTS pg_trgm;
			CREATE INDEX IF NOT EXISTS idx_document_chunks_embedding_hnsw
				ON document_chunks USING hnsw (embedding vector_cosine_ops);
			CREATE INDEX IF NOT EXISTS trgm_idx_title ON documents USING gin (title gin_trgm_ops);
		`},
		{"document_votes", `
			CREATE TABLE IF NOT EXISTS document_votes (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
				created_at TIMESTAMPTZ DEFAULT NOW(),
				UNIQUE(user_id, document_id)
			);
			CREATE INDEX IF NOT EXISTS idx_doc_votes_doc ON document_votes(document_id);
		`},
		{"api_key_quotas", `
			CREATE TABLE IF NOT EXISTS api_key_quotas (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				api_key_id TEXT NOT NULL,
				provider TEXT NOT NULL,
				rpd_used BIGINT DEFAULT 0,
				monthly_token_used BIGINT DEFAULT 0,
				last_used TIMESTAMPTZ DEFAULT NOW(),
				updated_at TIMESTAMPTZ DEFAULT NOW(),
				UNIQUE(api_key_id, provider)
			);
		`},
		{"search_histories", `
			CREATE TABLE IF NOT EXISTS search_histories (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				query TEXT NOT NULL,
				created_at TIMESTAMPTZ DEFAULT NOW(),
				UNIQUE(user_id, query)
			);
			CREATE INDEX IF NOT EXISTS idx_search_histories_user_id ON search_histories(user_id, created_at DESC);
		`},
		{"group rooms and members", `
			CREATE TABLE IF NOT EXISTS group_rooms (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				invite_code VARCHAR(12) UNIQUE NOT NULL,
				host_id UUID REFERENCES users(id) ON DELETE SET NULL,
				name VARCHAR(100),
				max_members INT DEFAULT 5,
				status VARCHAR(10) DEFAULT 'active',
				created_at TIMESTAMPTZ DEFAULT now(),
				closed_at TIMESTAMPTZ
			);

			CREATE TABLE IF NOT EXISTS group_room_members (
				room_id UUID REFERENCES group_rooms(id) ON DELETE CASCADE,
				user_id UUID REFERENCES users(id) ON DELETE CASCADE,
				joined_at TIMESTAMPTZ DEFAULT now(),
				left_at TIMESTAMPTZ,
				doc_count INT DEFAULT 0,
				is_host BOOLEAN DEFAULT false,
				PRIMARY KEY (room_id, user_id)
			);

			ALTER TABLE documents ADD COLUMN IF NOT EXISTS room_id UUID REFERENCES group_rooms(id) ON DELETE SET NULL;
			CREATE INDEX IF NOT EXISTS idx_documents_room ON documents(room_id) WHERE room_id IS NOT NULL;
		`},
		{"user_badges", `
			CREATE TABLE IF NOT EXISTS user_badges (
				user_id UUID NOT NULL,
				badge_id TEXT NOT NULL,
				earned_at TIMESTAMPTZ DEFAULT NOW(),
				PRIMARY KEY (user_id, badge_id)
			);
		`},
		{"user_streaks", `
			CREATE TABLE IF NOT EXISTS user_streaks (
				user_id UUID PRIMARY KEY,
				current_streak INT DEFAULT 0,
				longest_streak INT DEFAULT 0,
				last_activity_date DATE DEFAULT CURRENT_DATE
			);
		`},
		{"study_plans", `
			CREATE TABLE IF NOT EXISTS study_plans (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id UUID NOT NULL,
				name TEXT NOT NULL,
				exam_date DATE NOT NULL,
				doc_ids TEXT[] NOT NULL DEFAULT '{}',
				total_pages INT DEFAULT 0,
				pages_per_day INT DEFAULT 0,
				created_at TIMESTAMPTZ DEFAULT NOW()
			);
		`},
		{"room_messages", `
			CREATE TABLE IF NOT EXISTS room_messages (
				id TEXT PRIMARY KEY,
				room_id UUID NOT NULL,
				user_id UUID,
				user_name TEXT NOT NULL DEFAULT '',
				text TEXT NOT NULL,
				reply_to_id TEXT,
				mentions_ai BOOLEAN NOT NULL DEFAULT FALSE,
				is_ai BOOLEAN NOT NULL DEFAULT FALSE,
				timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);
			CREATE INDEX IF NOT EXISTS idx_room_messages_room_id ON room_messages(room_id, timestamp DESC);
		`},
		{"misc additive columns", `
			ALTER TABLE chat_histories ADD COLUMN IF NOT EXISTS title TEXT;
			ALTER TABLE users ADD COLUMN IF NOT EXISTS tier_expires_at TIMESTAMPTZ;
			ALTER TABLE payments ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ DEFAULT NOW();
			DO $$
			BEGIN
				IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='flashcards') THEN
					ALTER TABLE flashcards
						ADD COLUMN IF NOT EXISTS interval_days INT DEFAULT 1,
						ADD COLUMN IF NOT EXISTS ease_factor FLOAT DEFAULT 2.5,
						ADD COLUMN IF NOT EXISTS repetitions INT DEFAULT 0,
						ADD COLUMN IF NOT EXISTS next_review_at TIMESTAMPTZ DEFAULT NOW();
				END IF;
			END $$;
		`},
	}
}
