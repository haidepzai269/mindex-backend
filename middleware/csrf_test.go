package middleware

import (
	"mindex-backend/config"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireTrustedOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousOrigins := config.Env.CORSOrigins
	config.Env.CORSOrigins = []string{"http://localhost:3000"}
	t.Cleanup(func() {
		config.Env.CORSOrigins = previousOrigins
	})

	router := gin.New()
	router.Use(RequireTrustedOrigin())
	router.GET("/api/v1/protected", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.POST("/api/v1/protected", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.POST("/api/v1/billings/webhook", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	tests := []struct {
		name       string
		method     string
		path       string
		origin     string
		wantStatus int
	}{
		{name: "safe method passes without origin", method: http.MethodGet, path: "/api/v1/protected", wantStatus: http.StatusNoContent},
		{name: "unsafe method allows trusted origin", method: http.MethodPost, path: "/api/v1/protected", origin: "http://localhost:3000", wantStatus: http.StatusNoContent},
		{name: "unsafe method rejects missing origin", method: http.MethodPost, path: "/api/v1/protected", wantStatus: http.StatusForbidden},
		{name: "unsafe method rejects untrusted origin", method: http.MethodPost, path: "/api/v1/protected", origin: "https://evil.example", wantStatus: http.StatusForbidden},
		{name: "webhook is exempt", method: http.MethodPost, path: "/api/v1/billings/webhook", wantStatus: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}
