CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS chat_image_attachments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id VARCHAR(64) NOT NULL REFERENCES chat_histories(session_id) ON DELETE CASCADE,
    message_id TEXT,
    document_id UUID REFERENCES documents(id) ON DELETE SET NULL,
    collection_id UUID REFERENCES collections(id) ON DELETE SET NULL,
    original_name TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    width INT NOT NULL DEFAULT 0,
    height INT NOT NULL DEFAULT 0,
    storage_url TEXT NOT NULL,
    storage_public_id TEXT NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'done',
    error_message TEXT,
    ocr_text TEXT NOT NULL DEFAULT '',
    ocr_preview TEXT NOT NULL DEFAULT '',
    ocr_blocks JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '30 days',
    CONSTRAINT chat_image_attachments_status_check
        CHECK (status IN ('uploaded', 'analyzing', 'done', 'error')),
    CONSTRAINT chat_image_attachments_scope_check
        CHECK ((document_id IS NOT NULL AND collection_id IS NULL) OR (document_id IS NULL AND collection_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_chat_image_attachments_session
    ON chat_image_attachments(user_id, session_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_chat_image_attachments_expires
    ON chat_image_attachments(expires_at);
