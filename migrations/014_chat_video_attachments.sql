CREATE TABLE IF NOT EXISTS chat_video_attachments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id VARCHAR(64) NOT NULL REFERENCES chat_histories(session_id) ON DELETE CASCADE,
    message_id TEXT,
    original_name TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    duration_seconds INT NOT NULL DEFAULT 0,
    storage_url TEXT NOT NULL,
    storage_public_id TEXT NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'uploaded',
    error_message TEXT,
    has_audio BOOLEAN NOT NULL DEFAULT FALSE,
    audio_transcript TEXT NOT NULL DEFAULT '',
    frame_count INT NOT NULL DEFAULT 0,
    frame_analysis JSONB NOT NULL DEFAULT '[]'::jsonb,
    context_text TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '30 days',
    CONSTRAINT chat_video_attachments_status_check
        CHECK (status IN ('uploaded', 'processing', 'done', 'error'))
);

CREATE INDEX IF NOT EXISTS idx_chat_video_attachments_session
    ON chat_video_attachments(user_id, session_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_chat_video_attachments_expires
    ON chat_video_attachments(expires_at);
