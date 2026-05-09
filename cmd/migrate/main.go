package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env từ thư mục backend (chạy từ backend/)
	godotenv.Load()

	dbURL := os.Getenv("DATABASE_URL_CLOUD")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL_LOCAL")
	}
	if dbURL == "" {
		log.Fatal("Không tìm thấy DATABASE_URL")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		log.Fatalf("Không thể kết nối DB: %v", err)
	}
	defer conn.Close(ctx)

	migrations := []struct {
		name string
		sql  string
	}{
		{
			name: "add_tier_expires_at",
			sql:  `ALTER TABLE users ADD COLUMN IF NOT EXISTS tier_expires_at TIMESTAMPTZ`,
		},
		{
			name: "add_room_messages",
			sql: `CREATE TABLE IF NOT EXISTS room_messages (
				id          TEXT PRIMARY KEY,
				room_id     UUID NOT NULL,
				user_id     UUID,
				user_name   TEXT NOT NULL DEFAULT '',
				text        TEXT NOT NULL,
				reply_to_id TEXT,
				mentions_ai BOOLEAN NOT NULL DEFAULT FALSE,
				is_ai       BOOLEAN NOT NULL DEFAULT FALSE,
				timestamp   TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`,
		},
		{
			name: "idx_room_messages",
			sql:  `CREATE INDEX IF NOT EXISTS idx_room_messages_room_id ON room_messages(room_id, timestamp DESC)`,
		},
		{
			name: "add_chat_histories_title",
			sql:  `ALTER TABLE chat_histories ADD COLUMN IF NOT EXISTS title TEXT`,
		},
		{
			name: "add_flashcards_sm2",
			sql: `ALTER TABLE flashcards
				ADD COLUMN IF NOT EXISTS interval_days   INT DEFAULT 1,
				ADD COLUMN IF NOT EXISTS ease_factor     FLOAT DEFAULT 2.5,
				ADD COLUMN IF NOT EXISTS repetitions     INT DEFAULT 0,
				ADD COLUMN IF NOT EXISTS next_review_at  TIMESTAMPTZ DEFAULT NOW()`,
		},
		{
			name: "create_user_badges",
			sql: `CREATE TABLE IF NOT EXISTS user_badges (
				user_id    UUID NOT NULL,
				badge_id   TEXT NOT NULL,
				earned_at  TIMESTAMPTZ DEFAULT NOW(),
				PRIMARY KEY (user_id, badge_id)
			)`,
		},
		{
			name: "create_user_streaks",
			sql: `CREATE TABLE IF NOT EXISTS user_streaks (
				user_id            UUID PRIMARY KEY,
				current_streak     INT DEFAULT 0,
				longest_streak     INT DEFAULT 0,
				last_activity_date DATE DEFAULT CURRENT_DATE
			)`,
		},
		{
			name: "create_study_plans",
			sql: `CREATE TABLE IF NOT EXISTS study_plans (
				id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id      UUID NOT NULL,
				name         TEXT NOT NULL,
				exam_date    DATE NOT NULL,
				doc_ids      TEXT[] NOT NULL DEFAULT '{}',
				total_pages  INT DEFAULT 0,
				pages_per_day INT DEFAULT 0,
				created_at   TIMESTAMPTZ DEFAULT NOW()
			)`,
		},
		{
			name: "create_payments_updated_at",
			sql:  `ALTER TABLE payments ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ DEFAULT NOW()`,
		},
	}

	success := 0
	for _, m := range migrations {
		_, err := conn.Exec(ctx, m.sql)
		if err != nil {
			fmt.Printf("  ⚠️  [%-35s] %v\n", m.name, err)
		} else {
			fmt.Printf("  ✅ [%-35s] OK\n", m.name)
			success++
		}
	}

	fmt.Printf("\n🎉 Migration hoàn tất: %d/%d thành công\n", success, len(migrations))
}
