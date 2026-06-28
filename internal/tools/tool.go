package tools

import (
	"context"
	"encoding/json"
	"time"
)

// ToolCategory groups tools for permissioning/management (spec FR-007).
type ToolCategory string

const (
	CategoryInformationRetrieval ToolCategory = "information_retrieval"
	CategoryComputationExecution ToolCategory = "computation_execution"
	CategoryExternalActions      ToolCategory = "external_actions"
	CategoryContentGeneration    ToolCategory = "content_generation"
	CategoryMemoryContext        ToolCategory = "memory_context"
	CategoryOrchestration        ToolCategory = "orchestration"
)

// ToolExecutionContext carries runtime info into Execute. Constructed by the
// dispatcher per call from the authenticated chat request; never persisted.
type ToolExecutionContext struct {
	UserID    string
	SessionID string
	Tier      string
}

// ToolResult is what a tool returns. Exactly one of the fields below is
// expected to be populated, matching FR-014's required output kinds.
type ToolResult struct {
	TextOutput   string
	JSONOutput   json.RawMessage
	BinaryOutput []byte
	BinaryMIME   string
	FileURL      string
	RichContent  json.RawMessage
}

// Tool is the single extension point. A new tool implements this interface
// and is handed to Registry.Register - no dispatcher or chat code needs to
// change (FR-013).
type Tool interface {
	Name() string
	Description() string
	Category() ToolCategory
	// Parameters returns a minimal JSON-Schema-shaped definition, e.g.
	// {"type":"object","properties":{...},"required":[...]}.
	Parameters() map[string]interface{}
	Timeout() time.Duration
	// TierLimit returns nil for unlimited, or a map like {"FREE":6,"PRO":20}
	// meaning calls allowed per hour per tier (FR-012).
	TierLimit() map[string]int
	// RequiresConfirmation, if true, makes the dispatcher hold execution and
	// surface a pending-confirmation result instead of calling Execute
	// immediately (FR-009, SC-008).
	RequiresConfirmation() bool
	Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error)
}
