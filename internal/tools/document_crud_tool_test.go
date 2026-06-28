package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mindex-backend/config"
)

// testOwnedDocumentID is a real row this account owns (document_references.
// is_owner = TRUE) - rename_document's UPDATE has a real foreign-key/join
// dependency on document_references, so a fabricated ID would only prove the
// "not found" path, not the success path.
const testOwnedDocumentID = "e6a10f20-2aa5-413a-8b50-35f812c507c8"

func TestRenameDocumentToolRequiresConfirmation(t *testing.T) {
	tool := NewRenameDocumentTool()
	if !tool.RequiresConfirmation() {
		t.Fatalf("RenameDocumentTool.RequiresConfirmation() = false, want true (FR-009, SC-008)")
	}
}

func TestRenameDocumentToolRejectsUnownedDocument(t *testing.T) {
	memoryDBAvailable(t) // reuses the same lazy real-DB connection helper
	tool := NewRenameDocumentTool()
	input, _ := json.Marshal(map[string]string{"document_id": "00000000-0000-0000-0000-000000000000", "new_title": "x"})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{UserID: testMemoryUserID}, input)
	if err == nil {
		t.Fatalf("Execute() on a document the user doesn't own: want error, got nil")
	}
}

func TestRenameDocumentToolRenamesAndRestoresOwnedDocument(t *testing.T) {
	memoryDBAvailable(t)
	tool := NewRenameDocumentTool()
	execCtx := ToolExecutionContext{UserID: testMemoryUserID}

	var originalTitle string
	if err := config.DB.QueryRow(context.Background(),
		`SELECT title FROM documents WHERE id = $1`, testOwnedDocumentID).Scan(&originalTitle); err != nil {
		t.Fatalf("failed to read original title: %v", err)
	}
	defer func() {
		if _, err := config.DB.Exec(context.Background(),
			`UPDATE documents SET title = $1 WHERE id = $2`, originalTitle, testOwnedDocumentID); err != nil {
			t.Logf("failed to restore original title %q: %v", originalTitle, err)
		}
	}()

	newTitle := "[test-rename] " + originalTitle
	input, _ := json.Marshal(map[string]string{"document_id": testOwnedDocumentID, "new_title": newTitle})
	result, err := tool.Execute(context.Background(), execCtx, input)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if !strings.Contains(result.TextOutput, newTitle) {
		t.Fatalf("Execute() = %q, want it to mention the new title %q", result.TextOutput, newTitle)
	}

	var storedTitle string
	if err := config.DB.QueryRow(context.Background(),
		`SELECT title FROM documents WHERE id = $1`, testOwnedDocumentID).Scan(&storedTitle); err != nil {
		t.Fatalf("failed to read updated title: %v", err)
	}
	if storedTitle != newTitle {
		t.Fatalf("stored title = %q, want %q", storedTitle, newTitle)
	}
}

func TestDispatcherConfirmationFlowExecutesOnlyAfterConfirm(t *testing.T) {
	memoryDBAvailable(t)
	if config.RedisClient == nil {
		t.Skip("no Redis configured in this environment; skipping pending-confirmation flow test")
	}

	var originalTitle string
	if err := config.DB.QueryRow(context.Background(),
		`SELECT title FROM documents WHERE id = $1`, testOwnedDocumentID).Scan(&originalTitle); err != nil {
		t.Fatalf("failed to read original title: %v", err)
	}
	defer func() {
		if _, err := config.DB.Exec(context.Background(),
			`UPDATE documents SET title = $1 WHERE id = $2`, originalTitle, testOwnedDocumentID); err != nil {
			t.Logf("failed to restore original title %q: %v", originalTitle, err)
		}
	}()

	pendingID := "test-pending-" + testOwnedDocumentID
	newTitle := "[test-confirm-flow] " + originalTitle
	input, _ := json.Marshal(map[string]string{"document_id": testOwnedDocumentID, "new_title": newTitle})
	storePendingConfirmation(pendingID, "rename_document", input, ToolExecutionContext{UserID: testMemoryUserID})
	defer CancelPendingConfirmation(context.Background(), pendingID)

	// Wrong user must not be able to resolve someone else's pending action.
	reg := NewRegistry()
	RegisterDefaultTools(reg)
	d := NewDispatcher(reg)
	if _, err := d.ResolvePendingConfirmation(context.Background(), pendingID, "some-other-user-id"); err == nil {
		t.Fatalf("ResolvePendingConfirmation() with wrong user: want error, got nil")
	}

	// Title must be unchanged after the unauthorized attempt was rejected.
	var titleAfterWrongUser string
	_ = config.DB.QueryRow(context.Background(), `SELECT title FROM documents WHERE id = $1`, testOwnedDocumentID).Scan(&titleAfterWrongUser)
	if titleAfterWrongUser == newTitle {
		t.Fatalf("title was changed despite a wrong-user confirmation attempt")
	}

	// Re-store (the wrong-user attempt above consumed/deleted the record).
	storePendingConfirmation(pendingID, "rename_document", input, ToolExecutionContext{UserID: testMemoryUserID})
	rec, err := d.ResolvePendingConfirmation(context.Background(), pendingID, testMemoryUserID)
	if err != nil {
		t.Fatalf("ResolvePendingConfirmation() with correct user: unexpected error: %v", err)
	}
	if rec.Status != "success" {
		t.Fatalf("ResolvePendingConfirmation() status = %q, want %q (error: %s)", rec.Status, "success", rec.ErrorMessage)
	}

	var titleAfterConfirm string
	if err := config.DB.QueryRow(context.Background(), `SELECT title FROM documents WHERE id = $1`, testOwnedDocumentID).Scan(&titleAfterConfirm); err != nil {
		t.Fatalf("failed to read title after confirm: %v", err)
	}
	if titleAfterConfirm != newTitle {
		t.Fatalf("title after confirm = %q, want %q", titleAfterConfirm, newTitle)
	}

	// The pending record must be consumed - resolving it again must fail.
	if _, err := d.ResolvePendingConfirmation(context.Background(), pendingID, testMemoryUserID); err == nil {
		t.Fatalf("ResolvePendingConfirmation() replay: want error (already consumed), got nil")
	}
}
