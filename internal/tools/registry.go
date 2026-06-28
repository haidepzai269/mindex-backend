package tools

import (
	"fmt"
	"sync"
)

// Registry is the central store of registered tools (spec FR-001).
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Default is the process-wide registry used by the chat dispatcher.
// Populated by Init() at startup, mirroring the utils.AI singleton pattern.
var Default *Registry

// Register adds a tool. Returns an error if Name() is empty or already
// registered (fail-fast at startup, per US3 contract).
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return fmt.Errorf("tools: cannot register nil tool")
	}
	name := t.Name()
	if name == "" {
		return fmt.Errorf("tools: tool Name() must not be empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tools: tool %q already registered", name)
	}
	r.tools[name] = t
	return nil
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// ListByCategory returns all tools in a given category.
func (r *Registry) ListByCategory(category ToolCategory) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Tool
	for _, t := range r.tools {
		if t.Category() == category {
			out = append(out, t)
		}
	}
	return out
}

// All returns every registered tool.
func (r *Registry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// Init creates the Default registry and registers the built-in tools.
// Call once at backend startup, e.g. alongside utils.InitOrchestrator().
func Init() {
	Default = NewRegistry()
	RegisterDefaultTools(Default)
}
