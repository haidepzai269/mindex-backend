package tools

import "log"

// RegisterDefaultTools registers every built-in tool. Adding a new tool only
// requires implementing the Tool interface and adding one line here (US3,
// FR-013) - no other file needs to change.
func RegisterDefaultTools(r *Registry) {
	builtins := []Tool{
		NewCalculatorTool(),
		NewWebSearchTool(),
		NewRAGSearchTool(),
		NewWebFetchTool(),
		NewMemoryTool(),
		NewRenameDocumentTool(),
		NewFileGenerationTool(),
		NewCodeSandboxTool(),
		NewEchoTool(),
		NewWeatherTool(),
		NewNewsTool(),
		NewCryptoTool(),
	}
	for _, tool := range builtins {
		if err := r.Register(tool); err != nil {
			log.Printf("⚠️ [Tools] Failed to register %s: %v", tool.Name(), err)
		}
	}
}
