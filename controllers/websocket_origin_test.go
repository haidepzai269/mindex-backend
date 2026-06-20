package controllers

import (
	"mindex-backend/config"
	"net/http"
	"testing"
)

func TestWebSocketOriginChecks(t *testing.T) {
	previousOrigins := config.Env.CORSOrigins
	config.Env.CORSOrigins = []string{"http://localhost:3000"}
	t.Cleanup(func() {
		config.Env.CORSOrigins = previousOrigins
	})

	tests := []struct {
		name    string
		origin  string
		want    bool
		checker func(*http.Request) bool
	}{
		{name: "feedback allows whitelisted origin", origin: "http://localhost:3000", want: true, checker: upgrader.CheckOrigin},
		{name: "feedback rejects untrusted origin", origin: "https://evil.example", want: false, checker: upgrader.CheckOrigin},
		{name: "room allows whitelisted origin", origin: "http://localhost:3000", want: true, checker: roomWsUpgrader.CheckOrigin},
		{name: "room rejects untrusted origin", origin: "https://evil.example", want: false, checker: roomWsUpgrader.CheckOrigin},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "/ws", nil)
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			req.Header.Set("Origin", tt.origin)

			if got := tt.checker(req); got != tt.want {
				t.Fatalf("CheckOrigin(%q) = %v, want %v", tt.origin, got, tt.want)
			}
		})
	}
}
