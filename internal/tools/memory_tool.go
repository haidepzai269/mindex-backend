package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"mindex-backend/config"
)

// memoryEvictionCap bounds how many facts user_memory keeps per user; when
// exceeded, the lowest-importance/oldest rows are evicted first (Edge Case:
// "Memory đầy (quá nhiều fact về user)").
const memoryEvictionCap = 50

// MemoryTool lets the AI save, retrieve, forget, and list per-user facts
// (US6), strictly scoped to ToolExecutionContext.UserID (FR-011).
type MemoryTool struct{}

func NewMemoryTool() *MemoryTool { return &MemoryTool{} }

func (t *MemoryTool) Name() string { return "memory" }
func (t *MemoryTool) Description() string {
	return "Lưu, tìm lại, quên, hoặc liệt kê các thông tin cá nhân quan trọng về user (mục tiêu học tập, môn đang học...) để dùng lại ở các lượt chat hoặc session sau."
}
func (t *MemoryTool) Category() ToolCategory { return CategoryMemoryContext }
func (t *MemoryTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"save", "retrieve", "forget", "list"},
				"description": "save: lưu fact mới. retrieve: tìm fact liên quan tới query. forget: xóa fact khớp với query. list: liệt kê toàn bộ fact đã lưu.",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Nội dung fact cần lưu (chỉ dùng với action=save)",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Từ khóa để tìm/xóa fact liên quan (dùng với action=retrieve hoặc forget)",
			},
			"importance": map[string]interface{}{
				"type":        "integer",
				"description": "Mức độ quan trọng 1-10, mặc định 5 (chỉ dùng với action=save)",
			},
		},
		"required": []string{"action"},
	}
}
func (t *MemoryTool) Timeout() time.Duration     { return 5 * time.Second }
func (t *MemoryTool) TierLimit() map[string]int  { return nil }
func (t *MemoryTool) RequiresConfirmation() bool { return false }

type memoryInput struct {
	Action     string `json:"action"`
	Content    string `json:"content"`
	Query      string `json:"query"`
	Importance int    `json:"importance"`
}

func (t *MemoryTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	if strings.TrimSpace(execCtx.UserID) == "" {
		return ToolResult{}, fmt.Errorf("memory: thiếu user_id, không thể truy cập memory")
	}
	if config.DB == nil {
		return ToolResult{}, fmt.Errorf("memory: database chưa sẵn sàng")
	}

	var in memoryInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{}, fmt.Errorf("memory: invalid input: %v", err)
	}

	switch in.Action {
	case "save":
		return t.save(ctx, execCtx.UserID, in)
	case "retrieve":
		return t.retrieve(ctx, execCtx.UserID, in.Query)
	case "forget":
		return t.forget(ctx, execCtx.UserID, in.Query)
	case "list":
		return t.retrieve(ctx, execCtx.UserID, "")
	default:
		return ToolResult{}, fmt.Errorf("memory: action %q không hợp lệ (save|retrieve|forget|list)", in.Action)
	}
}

func (t *MemoryTool) save(ctx context.Context, userID string, in memoryInput) (ToolResult, error) {
	content := strings.TrimSpace(in.Content)
	if content == "" {
		return ToolResult{}, fmt.Errorf("memory: content không được rỗng khi save")
	}
	importance := in.Importance
	if importance < 1 || importance > 10 {
		importance = 5
	}

	if _, err := config.DB.Exec(ctx, `
		INSERT INTO user_memory (user_id, content, source, importance_score)
		VALUES ($1, $2, 'ai_decided', $3)`,
		userID, content, importance); err != nil {
		return ToolResult{}, fmt.Errorf("memory: lưu thất bại: %v", err)
	}

	evictOverflow(ctx, userID)

	return ToolResult{TextOutput: "Đã lưu thông tin: " + content}, nil
}

func (t *MemoryTool) retrieve(ctx context.Context, userID, query string) (ToolResult, error) {
	var rows pgx.Rows
	var err error

	if strings.TrimSpace(query) == "" {
		rows, err = config.DB.Query(ctx, `
			SELECT content, importance_score FROM user_memory
			WHERE user_id = $1
			ORDER BY importance_score DESC, created_at DESC
			LIMIT 20`, userID)
	} else {
		rows, err = config.DB.Query(ctx, `
			SELECT content, importance_score FROM user_memory
			WHERE user_id = $1 AND content ILIKE '%' || $2 || '%'
			ORDER BY importance_score DESC, created_at DESC
			LIMIT 20`, userID, query)
	}
	if err != nil {
		return ToolResult{}, fmt.Errorf("memory: truy xuất thất bại: %v", err)
	}
	defer rows.Close()

	var facts []string
	for rows.Next() {
		var content string
		var importance int
		if err := rows.Scan(&content, &importance); err != nil {
			return ToolResult{}, fmt.Errorf("memory: lỗi đọc dữ liệu: %v", err)
		}
		facts = append(facts, content)
	}
	if err := rows.Err(); err != nil {
		return ToolResult{}, fmt.Errorf("memory: lỗi đọc dữ liệu: %v", err)
	}

	if len(facts) == 0 {
		return ToolResult{TextOutput: "Không có thông tin nào được lưu."}, nil
	}
	return ToolResult{TextOutput: strings.Join(facts, "\n")}, nil
}

func (t *MemoryTool) forget(ctx context.Context, userID, query string) (ToolResult, error) {
	if strings.TrimSpace(query) == "" {
		return ToolResult{}, fmt.Errorf("memory: cần query để xác định fact cần quên")
	}
	tag, err := config.DB.Exec(ctx, `
		DELETE FROM user_memory WHERE user_id = $1 AND content ILIKE '%' || $2 || '%'`,
		userID, query)
	if err != nil {
		return ToolResult{}, fmt.Errorf("memory: xóa thất bại: %v", err)
	}
	if tag.RowsAffected() == 0 {
		return ToolResult{TextOutput: "Không tìm thấy thông tin phù hợp để quên."}, nil
	}
	return ToolResult{TextOutput: fmt.Sprintf("Đã quên %d thông tin liên quan.", tag.RowsAffected())}, nil
}

// evictOverflow keeps only the top memoryEvictionCap rows (by importance,
// then recency) for a user and deletes the rest - i.e. the lowest-importance/
// oldest facts are evicted first (Edge Case: "Memory đầy"). Best-effort:
// errors are not fatal to save().
func evictOverflow(ctx context.Context, userID string) {
	_, _ = config.DB.Exec(ctx, `
		DELETE FROM user_memory
		WHERE user_id = $1
		AND id NOT IN (
			SELECT id FROM user_memory
			WHERE user_id = $1
			ORDER BY importance_score DESC, created_at DESC
			LIMIT $2
		)`, userID, memoryEvictionCap)
}
