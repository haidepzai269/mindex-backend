package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"mindex-backend/config"
)

// RenameDocumentTool renames a document the user owns (US9's specific,
// concrete scope - "đổi tên tài liệu"). Side-effect tools that mutate user
// data always require explicit confirmation (FR-009, SC-008): the dispatcher
// holds execution behind DispatchResult.Pending until a separate confirm
// call resolves it (see Dispatcher.ResolvePendingConfirmation).
type RenameDocumentTool struct{}

func NewRenameDocumentTool() *RenameDocumentTool { return &RenameDocumentTool{} }

func (t *RenameDocumentTool) Name() string { return "rename_document" }
func (t *RenameDocumentTool) Description() string {
	return "Đổi tên một tài liệu mà user sở hữu. LUÔN cần user xác nhận trước khi thực thi."
}
func (t *RenameDocumentTool) Category() ToolCategory { return CategoryExternalActions }
func (t *RenameDocumentTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"document_id": map[string]interface{}{
				"type":        "string",
				"description": "ID của tài liệu cần đổi tên",
			},
			"new_title": map[string]interface{}{
				"type":        "string",
				"description": "Tên mới cho tài liệu",
			},
		},
		"required": []string{"document_id", "new_title"},
	}
}
func (t *RenameDocumentTool) Timeout() time.Duration     { return 5 * time.Second }
func (t *RenameDocumentTool) TierLimit() map[string]int  { return nil }
func (t *RenameDocumentTool) RequiresConfirmation() bool { return true }

type renameDocumentInput struct {
	DocumentID string `json:"document_id"`
	NewTitle   string `json:"new_title"`
}

func (t *RenameDocumentTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	var in renameDocumentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{}, fmt.Errorf("rename_document: invalid input: %v", err)
	}
	newTitle := strings.TrimSpace(in.NewTitle)
	if in.DocumentID == "" || newTitle == "" {
		return ToolResult{}, fmt.Errorf("rename_document: document_id và new_title không được rỗng")
	}
	if strings.TrimSpace(execCtx.UserID) == "" {
		return ToolResult{}, fmt.Errorf("rename_document: thiếu user_id")
	}
	if config.DB == nil {
		return ToolResult{}, fmt.Errorf("rename_document: database chưa sẵn sàng")
	}

	// Only the owner (document_references.is_owner) may rename - same
	// ownership pattern already used by controllers.UpdateDocumentPersona.
	result, err := config.DB.Exec(ctx, `
		UPDATE documents d
		SET title = $1
		FROM document_references dr
		WHERE d.id = dr.document_id AND d.id = $2 AND dr.user_id = $3 AND dr.is_owner = TRUE`,
		newTitle, in.DocumentID, execCtx.UserID)
	if err != nil {
		return ToolResult{}, fmt.Errorf("rename_document: lỗi cập nhật: %v", err)
	}
	if result.RowsAffected() == 0 {
		return ToolResult{}, fmt.Errorf("rename_document: không tìm thấy tài liệu hoặc bạn không có quyền đổi tên")
	}

	return ToolResult{TextOutput: fmt.Sprintf("Đã đổi tên tài liệu thành %q", newTitle)}, nil
}
