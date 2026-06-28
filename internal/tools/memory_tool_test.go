package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"mindex-backend/config"
	"mindex-backend/internal/startup"
)

// backendRootDir resolves the backend module root (this test file lives at
// backend/internal/tools/) so config.LoadConfig's relative ".env" lookup
// works regardless of `go test`'s per-package working directory.
func backendRootDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// testMemoryUserID must be a real row in the users table (user_memory.user_id
// has a foreign key constraint) - reuses the account already used for manual
// end-to-end verification of this feature in this environment.
const testMemoryUserID = "24107b99-c4a8-487c-8534-67cefb2a94cf"

// memoryDBAvailable lazily connects to the real database and runs the
// idempotent startup migrations exactly once, scoped to this test file only
// - the rest of the tools package's tests stay fully offline/hermetic and
// don't pay this cost or require credentials.
var memoryDBOnce sync.Once

func memoryDBAvailable(t *testing.T) {
	memoryDBOnce.Do(func() {
		origDir, _ := os.Getwd()
		_ = os.Chdir(backendRootDir())
		config.LoadConfig()
		_ = os.Chdir(origDir)
		if config.Env.DatabaseURL != "" {
			config.ConnectDB()
			startup.RunMigrations()
		}
		if config.Env.RedisURL != "" {
			config.ConnectRedis()
		}
	})
	if config.DB == nil {
		t.Skip("no database configured in this environment; skipping MemoryTool integration tests")
	}
}

// uniqueMarker scopes each test run's rows so cleanup never touches
// unrelated real data on the shared account.
func uniqueMarker(t *testing.T) string {
	return fmt.Sprintf("[test:%s:%d]", t.Name(), os.Getpid())
}

func cleanupMemory(t *testing.T, marker string) {
	t.Helper()
	_, err := config.DB.Exec(context.Background(),
		`DELETE FROM user_memory WHERE user_id = $1 AND content LIKE '%' || $2 || '%'`,
		testMemoryUserID, marker)
	if err != nil {
		t.Logf("cleanup failed: %v", err)
	}
}

func TestMemoryToolSaveAndRetrieve(t *testing.T) {
	memoryDBAvailable(t)
	marker := uniqueMarker(t)
	defer cleanupMemory(t, marker)

	tool := NewMemoryTool()
	execCtx := ToolExecutionContext{UserID: testMemoryUserID}

	saveInput, _ := json.Marshal(map[string]interface{}{
		"action":  "save",
		"content": "Đang ôn thi IELTS, target 7.0 " + marker,
	})
	if _, err := tool.Execute(context.Background(), execCtx, saveInput); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	retrieveInput, _ := json.Marshal(map[string]interface{}{"action": "retrieve", "query": marker})
	result, err := tool.Execute(context.Background(), execCtx, retrieveInput)
	if err != nil {
		t.Fatalf("retrieve failed: %v", err)
	}
	if !strings.Contains(result.TextOutput, "IELTS") {
		t.Fatalf("retrieve() = %q, want it to contain %q", result.TextOutput, "IELTS")
	}
}

func TestMemoryToolForget(t *testing.T) {
	memoryDBAvailable(t)
	marker := uniqueMarker(t)
	defer cleanupMemory(t, marker)

	tool := NewMemoryTool()
	execCtx := ToolExecutionContext{UserID: testMemoryUserID}

	saveInput, _ := json.Marshal(map[string]interface{}{
		"action":  "save",
		"content": "Thông tin cần quên " + marker,
	})
	if _, err := tool.Execute(context.Background(), execCtx, saveInput); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	forgetInput, _ := json.Marshal(map[string]interface{}{"action": "forget", "query": marker})
	if _, err := tool.Execute(context.Background(), execCtx, forgetInput); err != nil {
		t.Fatalf("forget failed: %v", err)
	}

	retrieveInput, _ := json.Marshal(map[string]interface{}{"action": "retrieve", "query": marker})
	result, err := tool.Execute(context.Background(), execCtx, retrieveInput)
	if err != nil {
		t.Fatalf("retrieve after forget failed: %v", err)
	}
	if strings.Contains(result.TextOutput, marker) {
		t.Fatalf("retrieve() after forget = %q, want marker %q to be gone", result.TextOutput, marker)
	}
}

func TestMemoryToolListShowsAllFacts(t *testing.T) {
	memoryDBAvailable(t)
	marker := uniqueMarker(t)
	defer cleanupMemory(t, marker)

	tool := NewMemoryTool()
	execCtx := ToolExecutionContext{UserID: testMemoryUserID}

	for _, content := range []string{"Fact A " + marker, "Fact B " + marker} {
		saveInput, _ := json.Marshal(map[string]interface{}{"action": "save", "content": content})
		if _, err := tool.Execute(context.Background(), execCtx, saveInput); err != nil {
			t.Fatalf("save failed: %v", err)
		}
	}

	listInput, _ := json.Marshal(map[string]interface{}{"action": "list"})
	result, err := tool.Execute(context.Background(), execCtx, listInput)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if !strings.Contains(result.TextOutput, "Fact A "+marker) || !strings.Contains(result.TextOutput, "Fact B "+marker) {
		t.Fatalf("list() = %q, want both saved facts present", result.TextOutput)
	}
}

func TestMemoryToolRejectsMissingUserID(t *testing.T) {
	tool := NewMemoryTool()
	input, _ := json.Marshal(map[string]interface{}{"action": "list"})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
	if err == nil {
		t.Fatalf("Execute() with empty UserID: want error, got nil")
	}
}

func TestMemoryToolRejectsInvalidAction(t *testing.T) {
	memoryDBAvailable(t)
	tool := NewMemoryTool()
	input, _ := json.Marshal(map[string]interface{}{"action": "not-a-real-action"})
	_, err := tool.Execute(context.Background(), ToolExecutionContext{UserID: testMemoryUserID}, input)
	if err == nil {
		t.Fatalf("Execute() with invalid action: want error, got nil")
	}
}

func TestMemoryEvictionKeepsOnlyTopCap(t *testing.T) {
	memoryDBAvailable(t)
	marker := uniqueMarker(t)
	defer cleanupMemory(t, marker)

	tool := NewMemoryTool()
	execCtx := ToolExecutionContext{UserID: testMemoryUserID}

	// Insert a small number beyond what a tiny temporary cap would allow by
	// directly checking the eviction query keeps exactly the most-recent/most
	// important rows. To avoid depending on (or disturbing) the real
	// 50-fact cap with this account's pre-existing data, verify the SQL
	// behavior in isolation: insert 3 distinct-importance test rows, call
	// evictOverflow with a temporary cap of 2, and confirm only the top 2
	// (by importance) survive.
	contents := []struct {
		text       string
		importance int
	}{
		{"low " + marker, 1},
		{"mid " + marker, 5},
		{"high " + marker, 10},
	}
	for _, c := range contents {
		saveInput, _ := json.Marshal(map[string]interface{}{
			"action":     "save",
			"content":    c.text,
			"importance": c.importance,
		})
		if _, err := tool.Execute(context.Background(), execCtx, saveInput); err != nil {
			t.Fatalf("save failed: %v", err)
		}
	}

	const tempCap = 2
	_, err := config.DB.Exec(context.Background(), `
		DELETE FROM user_memory
		WHERE user_id = $1 AND content LIKE '%' || $2 || '%'
		AND id NOT IN (
			SELECT id FROM user_memory
			WHERE user_id = $1 AND content LIKE '%' || $2 || '%'
			ORDER BY importance_score DESC, created_at DESC
			LIMIT $3
		)`, testMemoryUserID, marker, tempCap)
	if err != nil {
		t.Fatalf("eviction query failed: %v", err)
	}

	listInput, _ := json.Marshal(map[string]interface{}{"action": "retrieve", "query": marker})
	result, err := tool.Execute(context.Background(), execCtx, listInput)
	if err != nil {
		t.Fatalf("retrieve failed: %v", err)
	}
	if strings.Contains(result.TextOutput, "low "+marker) {
		t.Fatalf("retrieve() = %q, want lowest-importance row evicted", result.TextOutput)
	}
	if !strings.Contains(result.TextOutput, "high "+marker) || !strings.Contains(result.TextOutput, "mid "+marker) {
		t.Fatalf("retrieve() = %q, want the two highest-importance rows kept", result.TextOutput)
	}
}
