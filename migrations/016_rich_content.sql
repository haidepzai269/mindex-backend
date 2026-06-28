-- Migration 016: Add rich_content column to ai_response_logs
-- Stores structured JSON snapshots of tool results (weather cards, news carousels, crypto charts)
-- for rendering when user scrolls back in chat history.

ALTER TABLE ai_response_logs ADD COLUMN IF NOT EXISTS rich_content JSONB DEFAULT NULL;
