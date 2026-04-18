-- Migration 004: Group Study Chat
-- Tạo bảng group_rooms và group_room_members
-- Cập nhật bảng documents với cột room_id

-- Bảng quản lý phòng chat nhóm
CREATE TABLE IF NOT EXISTS group_rooms (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  invite_code  VARCHAR(12) UNIQUE NOT NULL,
  host_id      UUID REFERENCES users(id) ON DELETE SET NULL,
  name         VARCHAR(100) NOT NULL,
  max_members  INT DEFAULT 5,
  status       VARCHAR(10) DEFAULT 'active', -- active | closed | archived
  created_at   TIMESTAMPTZ DEFAULT now(),
  closed_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_group_rooms_invite_code ON group_rooms(invite_code);
CREATE INDEX IF NOT EXISTS idx_group_rooms_host ON group_rooms(host_id);

-- Bảng thành viên trong phòng
CREATE TABLE IF NOT EXISTS group_room_members (
  room_id    UUID REFERENCES group_rooms(id) ON DELETE CASCADE,
  user_id    UUID REFERENCES users(id) ON DELETE CASCADE,
  joined_at  TIMESTAMPTZ DEFAULT now(),
  left_at    TIMESTAMPTZ,  -- NULL = đang trong phòng, NOT NULL = đã rời
  doc_count  INT DEFAULT 0, -- Giới hạn 3 doc/user/room
  is_host    BOOLEAN DEFAULT false,
  PRIMARY KEY (room_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_group_room_members_user ON group_room_members(user_id);

-- Thêm cột room_id vào bảng documents (NULL = tài liệu cá nhân)
ALTER TABLE documents ADD COLUMN IF NOT EXISTS room_id UUID REFERENCES group_rooms(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_documents_room ON documents(room_id) WHERE room_id IS NOT NULL;
