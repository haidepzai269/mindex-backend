ALTER TABLE documents ADD COLUMN IF NOT EXISTS cloudinary_public_id TEXT;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS processing_error_code TEXT;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS processing_error_message TEXT;

CREATE TABLE IF NOT EXISTS upload_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    local_path TEXT,
    cloudinary_url TEXT,
    cloudinary_public_id TEXT,
    status VARCHAR(20) NOT NULL DEFAULT 'queued',
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_at TIMESTAMPTZ,
    locked_by TEXT,
    error_code TEXT,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(document_id),
    CONSTRAINT upload_jobs_status_check CHECK (status IN ('queued', 'running', 'retrying', 'succeeded', 'failed'))
);

CREATE INDEX IF NOT EXISTS idx_upload_jobs_claim
    ON upload_jobs(status, next_run_at, created_at)
    WHERE status IN ('queued', 'retrying');
CREATE INDEX IF NOT EXISTS idx_upload_jobs_document_id ON upload_jobs(document_id);
