package config

import "testing"

func TestIsAllowedOrigin(t *testing.T) {
	previous := Env.CORSOrigins
	Env.CORSOrigins = []string{
		"http://localhost:3000",
		"https://app.example.com/",
	}
	t.Cleanup(func() {
		Env.CORSOrigins = previous
	})

	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{name: "allows exact origin", origin: "http://localhost:3000", want: true},
		{name: "allows trailing slash", origin: "https://app.example.com/", want: true},
		{name: "rejects unknown origin", origin: "https://evil.example", want: false},
		{name: "rejects empty origin", origin: "", want: false},
		{name: "rejects path", origin: "https://app.example.com/path", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAllowedOrigin(tt.origin); got != tt.want {
				t.Fatalf("IsAllowedOrigin(%q) = %v, want %v", tt.origin, got, tt.want)
			}
		})
	}
}
