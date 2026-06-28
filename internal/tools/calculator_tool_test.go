package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCalculatorToolExecute(t *testing.T) {
	tool := NewCalculatorTool()
	tests := []struct {
		name       string
		expression string
		want       string
		wantErr    bool
	}{
		{name: "basic multiplication", expression: "1247 * 893", want: "1113571"},
		{name: "addition and subtraction", expression: "10 + 5 - 3", want: "12"},
		{name: "parentheses and precedence", expression: "(2 + 3) * 4", want: "20"},
		{name: "decimal division", expression: "7 / 2", want: "3.5"},
		{name: "division by zero rejected", expression: "1 / 0", wantErr: true},
		{name: "unsafe characters rejected", expression: "1; os.Exit(1)", wantErr: true},
		{name: "empty expression rejected", expression: "", wantErr: true},
		{name: "percentage VAT (US4 independent test)", expression: "15% * 2350000", want: "352500"},
		{name: "bare percentage", expression: "50%", want: "0.5"},
		{name: "percentage combined with parentheses", expression: "(10 + 20)% * 100", want: "30"},
		{name: "length conversion km to m", expression: "10 km to m", want: "10000"},
		{name: "weight conversion kg to g", expression: "5 kg to g", want: "5000"},
		{name: "volume conversion l to ml", expression: "1.5 l to ml", want: "1500"},
		{name: "mismatched unit categories rejected", expression: "10 km to kg", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, _ := json.Marshal(map[string]string{"expression": tt.expression})
			result, err := tool.Execute(context.Background(), ToolExecutionContext{}, input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Execute(%q) expected error, got result %q", tt.expression, result.TextOutput)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute(%q) unexpected error: %v", tt.expression, err)
			}
			if result.TextOutput != tt.want {
				t.Fatalf("Execute(%q) = %q, want %q", tt.expression, result.TextOutput, tt.want)
			}
		})
	}
}
