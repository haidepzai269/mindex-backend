package controllers

import "testing"

func TestSerializeSessionMessageDeletedTombstone(t *testing.T) {
	msg := map[string]interface{}{
		"id":         "msg-1",
		"role":       "assistant",
		"content":    "secret",
		"timestamp":  "2026-06-07T10:00:00Z",
		"log_id":     "log-1",
		"deleted_at": "2026-06-07T10:05:00Z",
		"sources":    []map[string]interface{}{{"title": "A"}},
	}

	got := serializeSessionMessage(msg)

	if got["is_deleted"] != true {
		t.Fatalf("is_deleted = %v, want true", got["is_deleted"])
	}
	if _, exists := got["content"]; exists {
		t.Fatalf("content should be omitted for tombstone: %#v", got)
	}
	if _, exists := got["log_id"]; exists {
		t.Fatalf("log_id should be omitted for tombstone: %#v", got)
	}
	if _, exists := got["sources"]; exists {
		t.Fatalf("sources should be omitted for tombstone: %#v", got)
	}
}

func TestSerializeSessionMessageActive(t *testing.T) {
	msg := map[string]interface{}{
		"id":        "msg-2",
		"role":      "user",
		"content":   "hello",
		"timestamp": "2026-06-07T10:00:00Z",
	}

	got := serializeSessionMessage(msg)

	if got["is_deleted"] != false {
		t.Fatalf("is_deleted = %v, want false", got["is_deleted"])
	}
	if got["content"] != "hello" {
		t.Fatalf("content = %v, want hello", got["content"])
	}
	if _, exists := got["deleted_at"]; exists {
		t.Fatalf("deleted_at should be omitted for active message: %#v", got)
	}
}

func TestBuildNormalChatRedisHistorySkipsDeletedAndOrphans(t *testing.T) {
	messages := []map[string]interface{}{
		{"id": "u1", "role": "user", "content": "question 1"},
		{"id": "a1", "role": "assistant", "content": "answer 1", "deleted_at": "2026-06-07T10:05:00Z"},
		{"id": "u2", "role": "user", "content": "question 2"},
		{"id": "a2", "role": "assistant", "content": "answer 2"},
		{"id": "u3", "role": "user", "content": "question 3", "deleted_at": "2026-06-07T10:10:00Z"},
		{"id": "a3", "role": "assistant", "content": "answer 3"},
		{"id": "u4", "role": "user", "content": "question 4"},
		{"id": "a4", "role": "assistant", "content": "answer 4"},
	}

	got := buildNormalChatRedisHistory(messages)

	if len(got) != 2 {
		t.Fatalf("len(history) = %d, want 2", len(got))
	}
	if got[0].Question != "question 2" || got[0].Answer != "answer 2" {
		t.Fatalf("history[0] = %#v, want question 2 / answer 2", got[0])
	}
	if got[1].Question != "question 4" || got[1].Answer != "answer 4" {
		t.Fatalf("history[1] = %#v, want question 4 / answer 4", got[1])
	}
}

func TestFilterShareableSessionMessagesSkipsDeletedAndSensitiveFields(t *testing.T) {
	messages := []map[string]interface{}{
		{
			"id":         "msg-1",
			"role":       "assistant",
			"content":    "keep me",
			"timestamp":  "2026-06-07T10:00:00Z",
			"log_id":     "log-1",
			"sources":    []map[string]interface{}{{"title": "A"}},
			"deleted_at": "2026-06-07T10:05:00Z",
		},
		{
			"id":        "msg-2",
			"role":      "user",
			"content":   "visible",
			"timestamp": "2026-06-07T10:10:00Z",
			"log_id":    "log-2",
		},
	}

	got := filterShareableSessionMessages(messages)

	if len(got) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(got))
	}
	if got[0]["content"] != "visible" {
		t.Fatalf("content = %v, want visible", got[0]["content"])
	}
	if _, exists := got[0]["log_id"]; exists {
		t.Fatalf("log_id should not be exposed in shared messages: %#v", got[0])
	}
}
