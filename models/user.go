package models

import (
	"time"
)

type User struct {
	ID                   string     `json:"id"`
	Email                string     `json:"email"`
	Name                 string     `json:"name"`
	Bio                  string     `json:"bio"`
	URLs                 []string   `json:"urls"`
	AvatarURL            string     `json:"avatar_url"`
	PasswordHash         string     `json:"-"`
	Role                 string     `json:"role"`
	Tier                 string     `json:"tier"`
	Persona              string     `json:"persona"`
	PersonaSet           bool       `json:"persona_set"`
	GoogleID             string     `json:"google_id,omitempty"`
	DisclaimerAcceptedAt *time.Time `json:"disclaimer_accepted_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
}

type UserQuota struct {
	PinnedDocs      int `json:"pinned_docs"`
	PinnedDocsLimit int `json:"pinned_docs_limit"`
	PublicDocs      int `json:"public_docs"`
	PublicDocsLimit int `json:"public_docs_limit"`
	BonusPinSlots   int `json:"bonus_pin_slots"`
}
