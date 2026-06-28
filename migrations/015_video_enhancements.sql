ALTER TABLE chat_video_attachments ADD COLUMN IF NOT EXISTS content_hash VARCHAR(64);

CREATE INDEX IF NOT EXISTS idx_chat_video_attachments_hash
    ON chat_video_attachments(content_hash) WHERE content_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS video_analysis_cache (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    content_hash VARCHAR(64) NOT NULL UNIQUE,
    has_audio BOOLEAN NOT NULL DEFAULT FALSE,
    audio_transcript TEXT NOT NULL DEFAULT '',
    frame_count INT NOT NULL DEFAULT 0,
    frame_analysis JSONB NOT NULL DEFAULT '[]'::jsonb,
    context_text TEXT NOT NULL DEFAULT '',
    original_duration INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
