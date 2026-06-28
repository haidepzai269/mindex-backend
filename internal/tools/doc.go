// Package tools implements the AI Tool Framework for Mindex.
//
// # Adding a new tool
//
// 1. Create a file (e.g. my_tool.go) implementing the Tool interface from tool.go
// 2. Add one line to RegisterDefaultTools in register.go
//
// No dispatcher, chat, or prompt code needs to change.
//
// See specs/008-ai-tool-framework/contracts/tool-interface.md for the full contract.
// See echo_tool.go for the simplest working example.
package tools
