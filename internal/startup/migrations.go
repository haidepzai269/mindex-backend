package startup

import (
	"log"
	"mindex-backend/config"
)

// RunMigrations chạy các DDL idempotent một lần duy nhất khi server khởi động.
// Tất cả CREATE TABLE / ALTER TABLE nên được đặt ở đây thay vì trong handlers.
func RunMigrations() {
	stmts := []struct {
		name string
		sql  string
	}{
		{"user_badges table", `
			CREATE TABLE IF NOT EXISTS user_badges (
				user_id   UUID NOT NULL,
				badge_id  TEXT NOT NULL,
				earned_at TIMESTAMPTZ DEFAULT NOW(),
				PRIMARY KEY (user_id, badge_id)
			)`},
		{"user_streaks table", `
			CREATE TABLE IF NOT EXISTS user_streaks (
				user_id            UUID PRIMARY KEY,
				current_streak     INT DEFAULT 0,
				longest_streak     INT DEFAULT 0,
				last_activity_date DATE DEFAULT CURRENT_DATE
			)`},
		{"study_plans table", `
			CREATE TABLE IF NOT EXISTS study_plans (
				id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id       UUID NOT NULL,
				name          TEXT NOT NULL,
				exam_date     DATE NOT NULL,
				doc_ids       TEXT[] NOT NULL DEFAULT '{}',
				total_pages   INT DEFAULT 0,
				pages_per_day INT DEFAULT 0,
				created_at    TIMESTAMPTZ DEFAULT NOW()
			)`},
		{"room_messages table", `
			CREATE TABLE IF NOT EXISTS room_messages (
				id          TEXT PRIMARY KEY,
				room_id     UUID NOT NULL,
				user_id     UUID,
				user_name   TEXT NOT NULL DEFAULT '',
				text        TEXT NOT NULL,
				reply_to_id TEXT,
				mentions_ai BOOLEAN NOT NULL DEFAULT FALSE,
				is_ai       BOOLEAN NOT NULL DEFAULT FALSE,
				timestamp   TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`},
		{"room_messages index", `
			CREATE INDEX IF NOT EXISTS idx_room_messages_room_id
			ON room_messages(room_id, timestamp DESC)`},
		{"chat_histories.title column", `
			ALTER TABLE chat_histories
			ADD COLUMN IF NOT EXISTS title TEXT`},
		{"users.tier_expires_at column", `
			ALTER TABLE users
			ADD COLUMN IF NOT EXISTS tier_expires_at TIMESTAMPTZ`},
		{"payments.updated_at column", `
			ALTER TABLE payments
			ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ DEFAULT NOW()`},
		{"flashcards SM-2 columns", `
			ALTER TABLE flashcards
			ADD COLUMN IF NOT EXISTS interval_days   INT DEFAULT 1,
			ADD COLUMN IF NOT EXISTS ease_factor     FLOAT DEFAULT 2.5,
			ADD COLUMN IF NOT EXISTS repetitions     INT DEFAULT 0,
			ADD COLUMN IF NOT EXISTS next_review_at  TIMESTAMPTZ DEFAULT NOW()`},
	}

	ok, fail := 0, 0
	for _, s := range stmts {
		if _, err := config.DB.Exec(config.Ctx, s.sql); err != nil {
			log.Printf("⚠️  [Migration] %s: %v", s.name, err)
			fail++
		} else {
			ok++
		}
	}
	log.Printf("✅ [Startup Migrations] %d OK, %d failed", ok, fail)
}
