package middleware

import (
	"mindex-backend/config"
	"mindex-backend/utils"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAuthMiddlewareAcceptsHeaderAndCookieButRejectsQueryToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousJWTSecret := config.Env.JWTSecret
	previousJWTRefreshSecret := config.Env.JWTRefreshSecret
	config.Env.JWTSecret = "test_access_secret_32_bytes_minimum"
	config.Env.JWTRefreshSecret = "test_refresh_secret_32_bytes_minimum"
	t.Cleanup(func() {
		config.Env.JWTSecret = previousJWTSecret
		config.Env.JWTRefreshSecret = previousJWTRefreshSecret
	})

	access, _, _, err := utils.GenerateTokenPair("user-1", "user", "student", false)
	if err != nil {
		t.Fatalf("GenerateTokenPair() error = %v", err)
	}

	tests := []struct {
		name       string
		target     string
		setup      func(*http.Request)
		wantStatus int
	}{
		{
			name:   "header",
			target: "/protected",
			setup: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+access)
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:   "cookie",
			target: "/protected",
			setup: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: "access_token", Value: access})
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "query token ignored",
			target:     "/protected?token=" + access,
			setup:      func(req *http.Request) {},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/protected", AuthMiddleware(), func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})

			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			tt.setup(req)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}
