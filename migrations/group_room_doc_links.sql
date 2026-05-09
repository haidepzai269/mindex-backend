-- Migration: Bảng junction để link tài liệu thư viện vào phòng học nhóm
-- Giải quyết vấn đề: documents.room_id là 1-1, không cho phép 1 doc ở nhiều room
-- hoặc vừa ở library vừa ở room

CREATE TABLE IF NOT EXISTS group_room_doc_links (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id     UUID        NOT NULL REFERENCES group_rooms(id) ON DELETE CASCADE,
    document_id UUID        NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    linked_by   UUID        NOT NULL REFERENCES users(id),
    linked_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (room_id, document_id)
);

CREATE INDEX IF NOT EXISTS idx_group_room_doc_links_room_id     ON group_room_doc_links(room_id);
CREATE INDEX IF NOT EXISTS idx_group_room_doc_links_document_id ON group_room_doc_links(document_id);
