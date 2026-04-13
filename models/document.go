package models

import "time"

type Document struct {
	ID                  string     `json:"id"`
	UserID              string     `json:"user_id"`
	Title               string     `json:"title"`
	FileHash            *string    `json:"file_hash,omitempty"`
	CloudinaryURL       *string    `json:"cloudinary_url,omitempty"`
	Status              string     `json:"status"` // queued, processing, ready, error
	IsPublic            bool       `json:"is_public"`
	IsSystem            bool       `json:"is_system"`
	ExpiredAt           *time.Time `json:"expired_at,omitempty"`
	QueryCount          int        `json:"query_count"`
	UpvoteCount         int        `json:"upvote_count"`
	ContributorRewarded bool       `json:"contributor_rewarded"`
	CreatorPersona      string     `json:"creator_persona"`
	CreatedAt           time.Time  `json:"created_at"`
	// UI/Logic fields
	Pinned     bool `json:"pinned"`
	ChunkCount int  `json:"chunk_count"`
}

type DocumentChunk struct {
	ID         string    `json:"id"`
	DocumentID string    `json:"document_id"`
	ChunkIndex int       `json:"chunk_index"`
	Content    string    `json:"content"`
	TokenCount int       `json:"token_count"`
	PageNumber int       `json:"page_number"`
	CreatedAt  time.Time `json:"created_at"`
}
