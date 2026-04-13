package persona

import (
	"context"
	"log"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PersonaConfig struct {
	Persona                string
	DisplayName            string
	DisplayEmoji           string
	DisplayDesc            string
	RequireDisclaimer      bool
	DisclaimerText         *string
	CommunitySubjectFilter []string
	PromptChat             string
	PromptSummaryQuick     string
	PromptSummaryDetailed  string
	PromptNoContext        string // SYS-013 Fallback
}

type PersonaCache struct {
	mu      sync.RWMutex
	configs map[string]PersonaConfig
}

var Cache = &PersonaCache{configs: make(map[string]PersonaConfig)}

func (c *PersonaCache) Load(db *pgxpool.Pool) error {
	ctx := context.Background()
	rows, err := db.Query(ctx, `
		SELECT persona, display_name, display_emoji,
			   display_desc, require_disclaimer, disclaimer_text,
			   community_subject_filter, prompt_chat,
			   prompt_summary_quick, prompt_summary_detailed, COALESCE(prompt_no_context, '')
		FROM persona_prompts`)
	if err != nil {
		return err
	}
	defer rows.Close()

	newMap := make(map[string]PersonaConfig)
	for rows.Next() {
		var cfg PersonaConfig
		err := rows.Scan(
			&cfg.Persona, &cfg.DisplayName, &cfg.DisplayEmoji,
			&cfg.DisplayDesc, &cfg.RequireDisclaimer, &cfg.DisclaimerText,
			&cfg.CommunitySubjectFilter, &cfg.PromptChat,
			&cfg.PromptSummaryQuick, &cfg.PromptSummaryDetailed, &cfg.PromptNoContext,
		)
		if err != nil {
			log.Println("Scan mismatch persona row", err)
			continue
		}
		newMap[cfg.Persona] = cfg
	}

	c.mu.Lock()
	c.configs = newMap
	c.mu.Unlock()
	return nil
}

func (c *PersonaCache) Get(p string) PersonaConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cfg, ok := c.configs[p]
	if !ok {
		return c.configs["student"] // fallback
	}
	return cfg
}

func (c *PersonaCache) GetChatPrompt(p string) string {
	return c.Get(p).PromptChat
}

func (c *PersonaCache) GetSubjectFilters(p string) []string {
	return c.Get(p).CommunitySubjectFilter
}

func (c *PersonaCache) GetAllPersonasForOnboarding() []map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Trả về thứ tự ưu tiên nhất định hoặc map list
	var list []map[string]interface{}
	for _, cfg := range c.configs {
		list = append(list, map[string]interface{}{
			"persona":            cfg.Persona,
			"display_name":       cfg.DisplayName,
			"display_emoji":      cfg.DisplayEmoji,
			"display_desc":       cfg.DisplayDesc,
			"require_disclaimer": cfg.RequireDisclaimer,
		})
	}
	return list
}
