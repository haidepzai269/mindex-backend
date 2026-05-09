-- Migration: Tạo bảng room_messages để lưu lịch sử chat phòng học nhóm vào PostgreSQL
-- Chạy một lần: psql $DATABASE_URL -f migrations/add_room_messages.sql

CREATE TABLE IF NOT EXISTS room_messages (
    id          TEXT PRIMARY KEY,
    room_id     UUID NOT NULL REFERENCES group_rooms(id) ON DELETE CASCADE,
    user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
    user_name   TEXT NOT NULL DEFAULT '',
    text        TEXT NOT NULL,
    reply_to_id TEXT,
    mentions_ai BOOLEAN NOT NULL DEFAULT FALSE,
    is_ai       BOOLEAN NOT NULL DEFAULT FALSE,
    timestamp   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_room_messages_room_id ON room_messages(room_id, timestamp DESC);
