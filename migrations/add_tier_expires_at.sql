-- Migration: Thêm tier_expires_at vào bảng users để track subscription expiry
ALTER TABLE users ADD COLUMN IF NOT EXISTS tier_expires_at TIMESTAMPTZ;
