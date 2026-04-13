package utils

import (
	"errors"
	"mindex-backend/config"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type JWTClaims struct {
	UserID  string `json:"user_id"`
	Role    string `json:"role"`
	Persona string `json:"persona"`
	jwt.RegisteredClaims
}

func GenerateTokenPair(userID, role, persona string) (string, string, error) {
	// 1. Access Token (Rút ngắn xuống 15 phút để bảo mật)
	accessID := uuid.New().String()
	accessClaims := JWTClaims{
		UserID:  userID,
		Role:    role,
		Persona: persona,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        accessID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessString, err := accessToken.SignedString([]byte(config.Env.JWTSecret))
	if err != nil {
		return "", "", err
	}

	// 2. Refresh Token (Giữ nguyên 7 ngày)
	refreshID := uuid.New().String()
	refreshClaims := JWTClaims{
		UserID:  userID,
		Role:    role,
		Persona: persona,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        refreshID,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshString, err := refreshToken.SignedString([]byte(config.Env.JWTRefreshSecret))
	if err != nil {
		return "", "", err
	}

	return accessString, refreshString, nil
}

func VerifyToken(tokenString string, isRefresh bool) (*JWTClaims, error) {
	secret := config.Env.JWTSecret
	if isRefresh {
		secret = config.Env.JWTRefreshSecret
	}

	token, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}
