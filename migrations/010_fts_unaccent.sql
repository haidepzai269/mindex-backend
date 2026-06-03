-- Migration 010: Rebuild content_tsv với unaccent để FTS hoạt động tốt hơn với tiếng Việt

-- 1. Enable unaccent extension
CREATE EXTENSION IF NOT EXISTS unaccent;

-- 2. Tạo wrapper IMMUTABLE cho unaccent vì unaccent() mặc định chỉ STABLE,
--    không dùng được trong GENERATED ALWAYS AS column.
CREATE OR REPLACE FUNCTION immutable_unaccent(text)
RETURNS text AS $$
    SELECT unaccent($1);
$$ LANGUAGE SQL IMMUTABLE PARALLEL SAFE;

-- 3. Drop cột generated cũ
ALTER TABLE document_chunks DROP COLUMN IF EXISTS content_tsv;

-- 4. Rebuild với immutable_unaccent
ALTER TABLE document_chunks
ADD COLUMN content_tsv tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', immutable_unaccent(content))) STORED;

-- 5. Rebuild GIN index
DROP INDEX IF EXISTS idx_chunks_fts;
CREATE INDEX idx_chunks_fts ON document_chunks USING GIN(content_tsv);
