package models

import (
	"time"
)

type Collection struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Emoji       string     `json:"emoji"`
	DocCount    int        `json:"doc_count"`
	LastChatAt  *time.Time `json:"last_chat_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Documents   []Document `json:"documents,omitempty"`
}

type CollectionDocument struct {
	CollectionID string    `json:"collection_id"`
	DocumentID   string    `json:"document_id"`
	AddedAt      time.Time `json:"added_at"`
	DisplayOrder int       `json:"display_order"`
}
