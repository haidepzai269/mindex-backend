-- Migration: RAG Upgrade SOTA
-- 1. Thêm cột tsvector vào document_chunks để hỗ trợ Hybrid Search
ALTER TABLE document_chunks 
ADD COLUMN IF NOT EXISTS content_tsv tsvector 
GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED;

-- 2. Tạo index GIN để tìm kiếm full-text nhanh
CREATE INDEX IF NOT EXISTS idx_chunks_fts ON document_chunks USING GIN(content_tsv);

-- 3. Tạo bảng document_intelligence để lưu "bản đồ tri thức" của tài liệu
CREATE TABLE IF NOT EXISTS document_intelligence (
    doc_id UUID PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
    main_topic TEXT,
    thesis TEXT,
    doc_type VARCHAR(50),
    outline JSONB,
    key_concepts JSONB,
    created_at TIMESTAMP DEFAULT NOW()
);

-- Index cho doc_id (đã có vì là PRIMARY KEY)
