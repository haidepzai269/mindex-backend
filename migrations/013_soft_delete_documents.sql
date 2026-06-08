-- Soft-delete column: khi user xóa tài liệu và không còn ai dùng,
-- giữ lại bản ghi + chunks 7 ngày để restore nếu upload lại cùng file.
ALTER TABLE documents ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ NULL DEFAULT NULL;

CREATE INDEX IF NOT EXISTS idx_documents_deleted_at ON documents (deleted_at) WHERE deleted_at IS NOT NULL;
