CREATE TABLE IF NOT EXISTS study_presentations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    doc_id UUID REFERENCES documents(id) ON DELETE CASCADE,
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    slides_data JSONB,
    status VARCHAR(20) DEFAULT 'pending' NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    CONSTRAINT study_presentations_status_check CHECK (((status)::text = ANY ((ARRAY['pending'::character varying, 'done'::character varying, 'failed'::character varying])::text[])))
);

CREATE INDEX IF NOT EXISTS idx_study_presentations_doc_user ON study_presentations(doc_id, user_id);
