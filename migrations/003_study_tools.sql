-- Migration 003: Flashcard Sets + Flashcards
-- Dùng cho P1 AI Study Tools

CREATE TABLE IF NOT EXISTS flashcard_sets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    doc_id      UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL,
    title       TEXT NOT NULL,
    card_count  INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


CREATE INDEX IF NOT EXISTS idx_flashcard_sets_doc_id  ON flashcard_sets(doc_id);
CREATE INDEX IF NOT EXISTS idx_flashcard_sets_user_id ON flashcard_sets(user_id);

CREATE TABLE IF NOT EXISTS flashcards (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    set_id      UUID NOT NULL REFERENCES flashcard_sets(id) ON DELETE CASCADE,
    front       TEXT NOT NULL,
    back        TEXT NOT NULL,
    difficulty  TEXT NOT NULL DEFAULT 'medium' CHECK (difficulty IN ('easy', 'medium', 'hard')),
    topic       TEXT,
    position    INTEGER NOT NULL DEFAULT 0,
    remembered  BOOLEAN NOT NULL DEFAULT FALSE, -- Trạng thái "nhớ rồi" của user
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_flashcards_set_id ON flashcards(set_id);

-- Migration 004: Quizzes + Questions + Attempts

CREATE TABLE IF NOT EXISTS quizzes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    doc_id      UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL,
    title       TEXT NOT NULL,
    config      JSONB NOT NULL DEFAULT '{}', -- {num_questions, type, difficulty, topic}
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


CREATE INDEX IF NOT EXISTS idx_quizzes_doc_id  ON quizzes(doc_id);
CREATE INDEX IF NOT EXISTS idx_quizzes_user_id ON quizzes(user_id);

CREATE TABLE IF NOT EXISTS quiz_questions (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    quiz_id        UUID NOT NULL REFERENCES quizzes(id) ON DELETE CASCADE,
    type           TEXT NOT NULL DEFAULT 'mcq' CHECK (type IN ('mcq', 'essay')),
    question       TEXT NOT NULL,
    options        JSONB,          -- Chỉ dùng cho MCQ: [{"text":..., "index":0}, ...]
    correct_index  INTEGER,        -- Index của đáp án đúng trong options
    explanation    TEXT,
    source_chunk_id INTEGER,
    position       INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_quiz_questions_quiz_id ON quiz_questions(quiz_id);

CREATE TABLE IF NOT EXISTS quiz_attempts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    quiz_id         UUID NOT NULL REFERENCES quizzes(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL,
    answers         JSONB NOT NULL DEFAULT '[]', -- [{question_id, answer, is_correct, score}]
    score           FLOAT NOT NULL DEFAULT 0,    -- Tễng điểm 0-100
    time_spent_sec  INTEGER NOT NULL DEFAULT 0,
    completed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);


CREATE INDEX IF NOT EXISTS idx_quiz_attempts_quiz_id  ON quiz_attempts(quiz_id);
CREATE INDEX IF NOT EXISTS idx_quiz_attempts_user_id  ON quiz_attempts(user_id);

-- Bảng quota học tập cho free/pro gate
-- Kiểm tra quiz_generations_today của user
CREATE TABLE IF NOT EXISTS study_quota (
    user_id            UUID PRIMARY KEY,
    quiz_gens_today    INTEGER NOT NULL DEFAULT 0,
    flashcard_gens_today INTEGER NOT NULL DEFAULT 0,
    last_reset_date    DATE NOT NULL DEFAULT CURRENT_DATE
);

