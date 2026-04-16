-- Migration 002: AI Response Logs + Response Ratings
-- Dùng cho P0 Response Quality Monitoring

-- Bảng ghi log toàn bộ AI responses
CREATE TABLE IF NOT EXISTS ai_response_logs (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id    TEXT NOT NULL,
    user_id       TEXT NOT NULL,
    document_id   TEXT,
    collection_id TEXT,
    question      TEXT NOT NULL,
    answer        TEXT NOT NULL,
    model_used    TEXT NOT NULL,         -- e.g. "ninerouter", "groq", "cerebras"
    latency_ms    INTEGER NOT NULL DEFAULT 0,
    token_count   INTEGER NOT NULL DEFAULT 0,
    sources_count INTEGER NOT NULL DEFAULT 0,
    topic_label   TEXT,                  -- Async: "định nghĩa", "so sánh", ...
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index cho query nhanh trong admin dashboard
CREATE INDEX IF NOT EXISTS idx_ai_logs_model_used  ON ai_response_logs(model_used);
CREATE INDEX IF NOT EXISTS idx_ai_logs_user_id     ON ai_response_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_ai_logs_created_at  ON ai_response_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ai_logs_session_id  ON ai_response_logs(session_id);

-- Bảng rating riêng biệt, liên kết với log
CREATE TABLE IF NOT EXISTS response_ratings (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    log_id     UUID NOT NULL REFERENCES ai_response_logs(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL,
    thumbs     BOOLEAN NOT NULL,          -- TRUE = thumbs up, FALSE = thumbs down
    rating     INTEGER CHECK (rating BETWEEN 1 AND 5),
    comment    TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (log_id, user_id)             -- Mỗi user chỉ rate 1 lần / log (có thể update)
);

CREATE INDEX IF NOT EXISTS idx_ratings_log_id    ON response_ratings(log_id);
CREATE INDEX IF NOT EXISTS idx_ratings_thumbs    ON response_ratings(thumbs);
CREATE INDEX IF NOT EXISTS idx_ratings_created   ON response_ratings(created_at DESC);
