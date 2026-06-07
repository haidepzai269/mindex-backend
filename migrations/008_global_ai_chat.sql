ALTER TABLE chat_histories
ADD COLUMN IF NOT EXISTS chat_scope TEXT NOT NULL DEFAULT 'document';

CREATE INDEX IF NOT EXISTS idx_chat_histories_global_ai
ON chat_histories(user_id, started_at DESC)
WHERE chat_scope = 'global_ai';
