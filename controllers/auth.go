package controllers

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"mindex-backend/config"
	"mindex-backend/internal/persona"
	"mindex-backend/models"
	"mindex-backend/utils"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/api/idtoken"
)

func generateOTP() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(900000))
	return fmt.Sprintf("%06d", n.Int64()+100000)
}

func setTokenCookies(c *gin.Context, access, refresh string, rememberMe bool) {
	// 1. Access Token: 15 phút (khớp với JWT)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "access_token",
		Value:    access,
		MaxAge:   900, // 15 phút
		Path:     "/",
		Domain:   "",
		Secure:   true, // Bắt buộc cho SameSite=None
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode,
	})

	// 2. Refresh Token
	if refresh != "" {
		maxAge := 7 * 24 * 3600 // Mặc định 7 ngày
		if rememberMe {
			maxAge = 10 * 24 * 3600 // 10 ngày nếu chọn Remember Me
		}

		http.SetCookie(c.Writer, &http.Cookie{
			Name:     "refresh_token",
			Value:    refresh,
			MaxAge:   maxAge,
			Path:     "/",
			Domain:   "",
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteNoneMode,
		})
	}
}

type RegisterReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Persona  string `json:"persona"` // Optional
}

type LoginReq struct {
	Email      string `json:"email" binding:"required"`
	Password   string `json:"password" binding:"required"`
	RememberMe bool   `json:"remember_me"`
}

func Register(c *gin.Context) {
	var req RegisterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Dữ liệu yêu cầu không hợp lệ"})
		return
	}

	req.Email = strings.TrimSpace(req.Email)
	req.Name = strings.TrimSpace(req.Name)

	if req.Name == "" {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Vui lòng nhập họ và tên"})
		return
	}

	if req.Email == "" {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Vui lòng nhập email sinh viên"})
		return
	}

	// Kiểm tra định dạng email cơ bản
	if !strings.Contains(req.Email, "@") || !strings.Contains(req.Email, ".") {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Email không đúng định dạng"})
		return
	}

	if len(req.Password) < 8 {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Mật khẩu phải chứa tối thiểu 8 ký tự"})
		return
	}

	if config.RedisClient == nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": "INTERNAL_ERROR", "message": "Không thể xử lý mật khẩu"})
		return
	}

	personaVal := req.Persona
	personaSet := true
	if personaVal == "" {
		personaVal = "student"
		personaSet = false
	}

	var userID string
	err = config.DB.QueryRow(
		config.Ctx,
		`INSERT INTO users (email, name, password_hash, persona, persona_set)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		req.Email, req.Name, string(hashed), personaVal, personaSet,
	).Scan(&userID)

	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			c.JSON(409, gin.H{"success": false, "error": "EMAIL_ALREADY_EXISTS", "message": "Email đã được đăng ký"})
			return
		}
		c.JSON(500, gin.H{"success": false, "message": "Internal server error"})
		return
	}

	// Cập nhật Bloom Filter
	if config.RedisClient == nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}

	access, refresh, refreshJTI, err := utils.GenerateTokenPair(userID, "user", personaVal, false)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": "TOKEN_GENERATION_FAILED", "message": "Không thể tạo phiên đăng nhập"})
		return
	}
	if setErr := config.RedisClient.Set(config.Ctx, "refresh_active:"+refreshJTI, userID, 7*24*time.Hour).Err(); setErr != nil {
		log.Printf("[Auth] Register: failed to store refresh JTI: %v", setErr)
		if _, delErr := config.DB.Exec(config.Ctx, "DELETE FROM users WHERE id = $1", userID); delErr != nil {
			log.Printf("[Auth] Register: failed to rollback user %s after refresh JTI store failure: %v", userID, delErr)
		}
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}
	if utils.EmailBloom != nil {
		utils.EmailBloom.Add(req.Email)
	}
	log.Printf("[AUDIT] REGISTER user_id=%s email=%s ip=%s", userID, req.Email, c.ClientIP())

	setTokenCookies(c, access, refresh, false)

	c.JSON(201, gin.H{
		"success": true,
		"data": gin.H{
			"access_token":     access,
			"refresh_token":    refresh,
			"user_id":          userID,
			"email":            req.Email,
			"name":             req.Name,
			"persona":          personaVal,
			"persona_set":      personaSet,
			"bio":              "",
			"urls":             []string{},
			"avatar_url":       "",
			"needs_onboarding": !personaSet,
		},
	})
}

func Login(c *gin.Context) {
	var req LoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Thiếu email hoặc password"})
		return
	}

	var user models.User
	err := config.DB.QueryRow(
		config.Ctx,
		`SELECT id, email, name, password_hash, COALESCE(role, 'user'), persona, persona_set, COALESCE(bio, ''), COALESCE(urls, '[]'), COALESCE(avatar_url, '')
		 FROM users WHERE email = $1`,
		req.Email,
	).Scan(&user.ID, &user.Email, &user.Name, &user.PasswordHash, &user.Role, &user.Persona, &user.PersonaSet, &user.Bio, &user.URLs, &user.AvatarURL)

	if err != nil || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		log.Printf("[AUDIT] LOGIN_FAILED email=%s ip=%s", req.Email, c.ClientIP())
		c.JSON(401, gin.H{"success": false, "error": "INVALID_CREDENTIALS", "message": "Sai email hoặc mật khẩu"})
		return
	}

	if config.RedisClient == nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}

	access, refresh, refreshJTI, err := utils.GenerateTokenPair(user.ID, user.Role, user.Persona, req.RememberMe)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": "TOKEN_GENERATION_FAILED", "message": "Không thể tạo phiên đăng nhập"})
		return
	}
	refreshTTL := 7 * 24 * time.Hour
	if req.RememberMe {
		refreshTTL = 10 * 24 * time.Hour
	}
	if setErr := config.RedisClient.Set(config.Ctx, "refresh_active:"+refreshJTI, user.ID, refreshTTL).Err(); setErr != nil {
		log.Printf("[Auth] Login: failed to store refresh JTI: %v", setErr)
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}
	log.Printf("[AUDIT] LOGIN_SUCCESS user_id=%s email=%s ip=%s", user.ID, user.Email, c.ClientIP())

	setTokenCookies(c, access, refresh, req.RememberMe)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"access_token":  access,
			"refresh_token": refresh,
			"remember_me":   req.RememberMe,
			"user": gin.H{
				"id":         user.ID,
				"name":       user.Name,
				"email":      user.Email,
				"role":       user.Role,
				"persona":    user.Persona,
				"bio":        user.Bio,
				"urls":       user.URLs,
				"avatar_url": user.AvatarURL,
			},
		},
	})
}

func Refresh(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}

	// Thử bind JSON trước
	c.ShouldBindJSON(&req)

	refreshToken := req.RefreshToken
	// Nếu body trống, thử lấy từ cookie
	if refreshToken == "" {
		cookieToken, err := c.Cookie("refresh_token")
		if err == nil {
			refreshToken = cookieToken
		}
	}

	if refreshToken == "" {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Thiếu token"})
		return
	}

	claims, err := utils.VerifyToken(refreshToken, true)
	if err != nil {
		c.JSON(401, gin.H{"success": false, "error": "INVALID_REFRESH_TOKEN", "message": "Token hết hạn hoặc không hợp lệ"})
		return
	}

	// Redis bắt buộc: cần để kiểm tra blacklist và revoke_before
	if config.RedisClient == nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}

	// Chặn reuse: kiểm tra refresh token JTI đã bị blacklist chưa
	blacklisted, err := config.RedisClient.Exists(config.Ctx, "blacklist:"+claims.ID).Result()
	if err != nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}
	if blacklisted > 0 {
		c.JSON(401, gin.H{"success": false, "error": "TOKEN_REUSE_DETECTED", "message": "Token đã được sử dụng"})
		return
	}

	// Chặn token cũ sau khi đổi/reset mật khẩu
	revokeAt, revokeErr := config.RedisClient.Get(config.Ctx, "revoke_before:"+claims.UserID).Int64()
	if revokeErr != nil && revokeErr != redis.Nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Authentication service unavailable"})
		return
	}
	if revokeErr == nil {
		if claims.IssuedAt.Unix() < revokeAt {
			c.JSON(401, gin.H{"success": false, "error": "TOKEN_REVOKED", "message": "Phiên đăng nhập đã bị thu hồi, vui lòng đăng nhập lại"})
			return
		}
	}

	// Kiểm tra JTI có trong whitelist active không
	activeUserID, err := config.RedisClient.Get(config.Ctx, "refresh_active:"+claims.ID).Result()
	if err != nil && err != redis.Nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Authentication service unavailable"})
		return
	}
	if err == redis.Nil || activeUserID != claims.UserID {
		c.JSON(401, gin.H{"success": false, "error": "TOKEN_REVOKED", "message": "Phiên đăng nhập không hợp lệ, vui lòng đăng nhập lại"})
		return
	}

	// Rotate: tạo cả access và refresh token mới
	// Phát hiện rememberMe từ TTL gốc: nếu token ban đầu > 8 ngày thì là remember_me=true
	rememberMe := !claims.IssuedAt.IsZero() && !claims.ExpiresAt.IsZero() &&
		claims.ExpiresAt.Sub(claims.IssuedAt.Time) > 8*24*time.Hour

	access, newRefresh, newRefreshJTI, err := utils.GenerateTokenPair(claims.UserID, claims.Role, claims.Persona, rememberMe)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": "TOKEN_GENERATION_FAILED", "message": "Không thể tạo token mới"})
		return
	}

	// Lưu JTI mới vào whitelist trước khi revoke cái cũ
	newRefreshTTL := 7 * 24 * time.Hour
	if rememberMe {
		newRefreshTTL = 10 * 24 * time.Hour
	}
	if setErr := config.RedisClient.Set(config.Ctx, "refresh_active:"+newRefreshJTI, claims.UserID, newRefreshTTL).Err(); setErr != nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}

	// Revoke token cũ: blacklist + xóa khỏi whitelist
	if ttl := time.Until(claims.ExpiresAt.Time); ttl > 0 {
		if setErr := config.RedisClient.Set(config.Ctx, "blacklist:"+claims.ID, "1", ttl).Err(); setErr != nil {
			log.Printf("[Auth] Refresh: failed to blacklist old refresh JTI %s: %v", claims.ID, setErr)
			if delErr := config.RedisClient.Del(config.Ctx, "refresh_active:"+newRefreshJTI).Err(); delErr != nil {
				log.Printf("[Auth] Refresh: failed to rollback new refresh JTI %s: %v", newRefreshJTI, delErr)
			}
			c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Authentication service unavailable"})
			return
		}
	}
	if delErr := config.RedisClient.Del(config.Ctx, "refresh_active:"+claims.ID).Err(); delErr != nil {
		log.Printf("[Auth] Refresh: failed to delete old refresh JTI %s: %v", claims.ID, delErr)
		if rollbackErr := config.RedisClient.Del(config.Ctx, "refresh_active:"+newRefreshJTI).Err(); rollbackErr != nil {
			log.Printf("[Auth] Refresh: failed to rollback new refresh JTI %s: %v", newRefreshJTI, rollbackErr)
		}
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Authentication service unavailable"})
		return
	}

	// Set cả hai cookie mới, giữ nguyên rememberMe preference
	setTokenCookies(c, access, newRefresh, rememberMe)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"access_token":  access,
			"refresh_token": newRefresh,
		},
	})
}

func GetMe(c *gin.Context) {
	userID := c.GetString("user_id")
	cacheKey := fmt.Sprintf("user:profile:%s", userID)

	// 1. Thử lấy từ Redis
	if config.RedisClient != nil {
		cachedData, err := config.RedisClient.Get(config.Ctx, cacheKey).Result()
		if err == nil {
			var result gin.H
			if err := json.Unmarshal([]byte(cachedData), &result); err == nil {
				c.JSON(200, gin.H{
					"success": true,
					"data":    result,
					"cache":   "HIT",
				})
				return
			}
		}
	}

	// 2. Nếu không có cache, truy vấn DB
	var user models.User
	var role string
	err := config.DB.QueryRow(config.Ctx, `
		SELECT id, name, email, persona, persona_set, disclaimer_accepted_at, COALESCE(bio, ''), COALESCE(urls, '[]'), COALESCE(avatar_url, ''), COALESCE(role, 'user'), COALESCE(tier, 'FREE')
		FROM users WHERE id = $1`, userID).
		Scan(&user.ID, &user.Name, &user.Email, &user.Persona, &user.PersonaSet, &user.DisclaimerAcceptedAt, &user.Bio, &user.URLs, &user.AvatarURL, &role, &user.Tier)
	if err != nil {
		c.JSON(404, gin.H{"success": false, "error": "USER_NOT_FOUND", "message": "Không tìm thấy user"})
		return
	}

	var pinnedDocs int
	config.DB.QueryRow(config.Ctx, `SELECT COUNT(*) FROM document_references WHERE user_id = $1 AND pinned = TRUE`, userID).Scan(&pinnedDocs)
	var publicDocs int
	config.DB.QueryRow(config.Ctx, `SELECT COUNT(*) FROM documents WHERE contributor_id = $1 AND is_public = TRUE`, userID).Scan(&publicDocs)

	bonusPinSlots := 0
	personaCfg := persona.Cache.Get(user.Persona)

	limitPins := 3
	limitShares := 3
	if user.Tier == "PRO" {
		limitPins = 5
		limitShares = 5
	} else if user.Tier == "ULTRA" {
		limitPins = 10
		limitShares = 10
	}

	responseData := gin.H{
		"id":                     user.ID,
		"name":                   user.Name,
		"email":                  user.Email,
		"role":                   role,
		"bio":                    user.Bio,
		"urls":                   user.URLs,
		"avatar_url":             user.AvatarURL,
		"persona":                user.Persona,
		"persona_set":            user.PersonaSet,
		"tier":                   user.Tier,
		"disclaimer_accepted_at": user.DisclaimerAcceptedAt,
		"persona_info": gin.H{
			"display_name":       personaCfg.DisplayName,
			"display_emoji":      personaCfg.DisplayEmoji,
			"display_desc":       personaCfg.DisplayDesc,
			"require_disclaimer": personaCfg.RequireDisclaimer,
		},
		"quota": models.UserQuota{
			PinnedDocs:      pinnedDocs,
			PinnedDocsLimit: limitPins + bonusPinSlots,
			PublicDocs:      publicDocs,
			PublicDocsLimit: limitShares,
			BonusPinSlots:   bonusPinSlots,
		},
	}

	// 3. Lưu vào Redis (TTL 30 phút)
	if config.RedisClient != nil {
		jsonData, err := json.Marshal(responseData)
		if err == nil {
			config.RedisClient.Set(config.Ctx, cacheKey, jsonData, 30*time.Minute)
		}
	}

	c.JSON(200, gin.H{
		"success": true,
		"data":    responseData,
		"cache":   "MISS",
	})
}

func UpdatePersona(c *gin.Context) {
	userID := c.GetString("user_id")
	var req struct {
		Persona            string `json:"persona"`
		DisclaimerAccepted bool   `json:"disclaimer_accepted"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Invalid request body"})
		return
	}

	validPersonas := map[string]bool{
		"student": true, "doctor": true, "legal": true,
		"engineer": true, "business": true, "researcher": true,
	}
	if !validPersonas[req.Persona] {
		c.JSON(400, gin.H{"success": false, "error": "INVALID_PERSONA", "message": "Persona không hợp lệ"})
		return
	}

	personaCfg := persona.Cache.Get(req.Persona)

	if personaCfg.RequireDisclaimer && !req.DisclaimerAccepted {
		c.JSON(400, gin.H{
			"success": false,
			"error":   "DISCLAIMER_REQUIRED",
			"message": "Persona này yêu cầu xác nhận điều khoản trước khi sử dụng.",
			"data": gin.H{
				"disclaimer_text": personaCfg.DisclaimerText,
			},
		})
		return
	}

	now := time.Now()
	var acceptedAt *time.Time
	if personaCfg.RequireDisclaimer {
		acceptedAt = &now
	}

	_, err := config.DB.Exec(config.Ctx, `
		UPDATE users 
		SET persona=$1, persona_set=TRUE, disclaimer_accepted_at=$2 
		WHERE id=$3`, req.Persona, acceptedAt, userID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Failed to update persona"})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"persona":                req.Persona,
			"persona_set":            true,
			"disclaimer_accepted_at": acceptedAt,
			"message":                "Đã chuyển sang chế độ " + personaCfg.DisplayName + " " + personaCfg.DisplayEmoji,
		},
	})
}

func GetOnboardingPersonas(c *gin.Context) {
	c.JSON(200, gin.H{
		"success": true,
		"data":    persona.Cache.GetAllPersonasForOnboarding(),
	})
}

func UpdateProfile(c *gin.Context) {
	userID := c.GetString("user_id")
	var req struct {
		Name      string   `json:"name"`
		Bio       string   `json:"bio"`
		URLs      []string `json:"urls"`
		AvatarURL string   `json:"avatar_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Dữ liệu không hợp lệ"})
		return
	}

	_, err := config.DB.Exec(config.Ctx, `
		UPDATE users 
		SET name=$1, bio=$2, urls=$3, avatar_url=$4 
		WHERE id=$5`, req.Name, req.Bio, req.URLs, req.AvatarURL, userID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể cập nhật hồ sơ"})
		return
	}

	// Xóa cache profile
	if config.RedisClient != nil {
		config.RedisClient.Del(config.Ctx, fmt.Sprintf("user:profile:%s", userID))
	}

	c.JSON(200, gin.H{
		"success": true,
		"message": "Cập nhật hồ sơ thành công",
		"data":    req,
	})
}

func ChangePassword(c *gin.Context) {
	userID := c.GetString("user_id")
	var req struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required,min=8"`
		OTPCode     string `json:"otp_code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Dữ liệu không hợp lệ hoặc mật khẩu quá ngắn"})
		return
	}

	// 1. Redis bắt buộc để xác thực OTP
	if config.RedisClient == nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}

	cacheKey := fmt.Sprintf("otp:password:%s", userID)
	storedOTP, err := config.RedisClient.Get(config.Ctx, cacheKey).Result()
	if err != nil || storedOTP != req.OTPCode {
		c.JSON(400, gin.H{"success": false, "error": "INVALID_OTP", "message": "Mã xác thực không chính xác hoặc đã hết hạn"})
		return
	}
	if delErr := config.RedisClient.Del(config.Ctx, cacheKey).Err(); delErr != nil {
		log.Printf("[Auth] ChangePassword: failed to delete OTP for user %s: %v", userID, delErr)
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Authentication service unavailable"})
		return
	}

	var hashedOld string
	err = config.DB.QueryRow(config.Ctx, "SELECT password_hash FROM users WHERE id = $1", userID).Scan(&hashedOld)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Lỗi hệ thống"})
		return
	}

	if bcrypt.CompareHashAndPassword([]byte(hashedOld), []byte(req.OldPassword)) != nil {
		c.JSON(401, gin.H{"success": false, "error": "INVALID_OLD_PASSWORD", "message": "Mật khẩu cũ không chính xác"})
		return
	}

	hashedNew, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": "INTERNAL_ERROR", "message": "Không thể xử lý mật khẩu"})
		return
	}
	_, err = config.DB.Exec(config.Ctx, "UPDATE users SET password_hash = $1 WHERE id = $2", string(hashedNew), userID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể cập nhật mật khẩu"})
		return
	}

	// Thu hồi tất cả refresh token cũ và xóa cache profile
	if setErr := config.RedisClient.Set(config.Ctx, "revoke_before:"+userID, time.Now().Unix(), 10*24*time.Hour).Err(); setErr != nil {
		log.Printf("[Auth] ChangePassword: failed to set revoke_before for user %s: %v", userID, setErr)
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Authentication service unavailable"})
		return
	}
	if delErr := config.RedisClient.Del(config.Ctx, fmt.Sprintf("user:profile:%s", userID)).Err(); delErr != nil {
		log.Printf("[Auth] ChangePassword: failed to clear profile cache for user %s: %v", userID, delErr)
	}

	log.Printf("[AUDIT] PASSWORD_CHANGE user_id=%s ip=%s", userID, c.ClientIP())
	c.JSON(200, gin.H{"success": true, "message": "Đã đổi mật khẩu thành công"})
}

// SendPasswordOTP gửi mã OTP cho người dùng đang đăng nhập để đổi mật khẩu
func SendPasswordOTP(c *gin.Context) {
	userID := c.GetString("user_id")

	if config.RedisClient == nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}

	var email string
	err := config.DB.QueryRow(config.Ctx, "SELECT email FROM users WHERE id = $1", userID).Scan(&email)
	if err != nil {
		c.JSON(404, gin.H{"success": false, "message": "Không tìm thấy người dùng"})
		return
	}

	otp := generateOTP()
	cacheKey := fmt.Sprintf("otp:password:%s", userID)
	if setErr := config.RedisClient.Set(config.Ctx, cacheKey, otp, 5*time.Minute).Err(); setErr != nil {
		log.Printf("[Auth] SendPasswordOTP: failed to store OTP: %v", setErr)
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}

	err = utils.SendOTPEmail(email, otp, "Đổi mật khẩu")
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể gửi email xác thực"})
		return
	}

	c.JSON(200, gin.H{"success": true, "message": "Mã xác thực đã được gửi tới email của bạn"})
}

// ForgotPasswordSendOTP gửi mã OTP cho người dùng quên mật khẩu (dùng email)
func ForgotPasswordSendOTP(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Email không hợp lệ"})
		return
	}

	if config.RedisClient == nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ gửi mã xác thực tạm thời không khả dụng"})
		return
	}

	// Luôn trả về 200 cùng message dù email tồn tại hay không (chống account enumeration).
	// OTP chỉ được tạo và gửi nếu email thực sự tồn tại.
	var userID string
	exists := config.DB.QueryRow(config.Ctx, "SELECT id FROM users WHERE email = $1", req.Email).Scan(&userID) == nil

	if exists {
		otp := generateOTP()
		cacheKey := fmt.Sprintf("otp:reset:%s", req.Email)
		if setErr := config.RedisClient.Set(config.Ctx, cacheKey, otp, 5*time.Minute).Err(); setErr != nil {
			log.Printf("[Auth] ForgotPassword: failed to store OTP for %s: %v", req.Email, setErr)
			c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Authentication service unavailable"})
			return
		}
		if err := utils.SendOTPEmail(req.Email, otp, "Khôi phục mật khẩu"); err != nil {
			log.Printf("[Auth] ForgotPassword: failed to send OTP to %s: %v", req.Email, err)
		}
	}

	c.JSON(200, gin.H{"success": true, "message": "Nếu email tồn tại trong hệ thống, mã khôi phục đã được gửi."})
}

// ResetPassword đặt lại mật khẩu bằng mã OTP
func ResetPassword(c *gin.Context) {
	var req struct {
		Email       string `json:"email" binding:"required,email"`
		OTPCode     string `json:"otp_code" binding:"required"`
		NewPassword string `json:"new_password" binding:"required,min=8"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Dữ liệu không hợp lệ hoặc mật khẩu quá ngắn"})
		return
	}

	// 1. Redis bắt buộc để xác thực OTP
	if config.RedisClient == nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}

	// Chống brute-force OTP: lock sau 5 lần sai
	failKey := fmt.Sprintf("otp:fail:%s", req.Email)
	failCount, _ := config.RedisClient.Get(config.Ctx, failKey).Int()
	if failCount >= 5 {
		c.JSON(429, gin.H{"success": false, "error": "OTP_LOCKED", "message": "Quá nhiều lần thử sai. Vui lòng yêu cầu mã mới sau 15 phút."})
		return
	}

	otpKey := fmt.Sprintf("otp:reset:%s", req.Email)
	storedOTP, err := config.RedisClient.Get(config.Ctx, otpKey).Result()
	if err != nil || storedOTP != req.OTPCode {
		config.RedisClient.Incr(config.Ctx, failKey)
		config.RedisClient.Expire(config.Ctx, failKey, 15*time.Minute)
		c.JSON(400, gin.H{"success": false, "message": "Mã xác thực không chính xác hoặc đã hết hạn"})
		return
	}

	// OTP đúng — xóa OTP + xóa bộ đếm fail
	config.RedisClient.Del(config.Ctx, failKey)
	if delErr := config.RedisClient.Del(config.Ctx, otpKey).Err(); delErr != nil {
		log.Printf("[Auth] ResetPassword: failed to delete OTP for %s: %v", req.Email, delErr)
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Authentication service unavailable"})
		return
	}

	// 2. Hash mật khẩu mới
	hashed, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": "INTERNAL_ERROR", "message": "Không thể xử lý mật khẩu"})
		return
	}

	// 3. Cập nhật DB, lấy user_id để thu hồi token
	var userID string
	err = config.DB.QueryRow(
		config.Ctx,
		"UPDATE users SET password_hash = $1 WHERE email = $2 RETURNING id",
		string(hashed), req.Email,
	).Scan(&userID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể cập nhật mật khẩu"})
		return
	}

	// 4. Thu hồi tất cả refresh token cũ
	if setErr := config.RedisClient.Set(config.Ctx, "revoke_before:"+userID, time.Now().Unix(), 10*24*time.Hour).Err(); setErr != nil {
		log.Printf("[AUDIT] PASSWORD_RESET_REVOKE_FAILED user_id=%s email=%s ip=%s err=%v", userID, req.Email, c.ClientIP(), setErr)
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Authentication service unavailable"})
		return
	}

	log.Printf("[AUDIT] PASSWORD_RESET email=%s ip=%s", req.Email, c.ClientIP())
	c.JSON(200, gin.H{"success": true, "message": "Mật khẩu đã được đặt lại thành công"})
}

func Logout(c *gin.Context) {
	userID := c.GetString("user_id")
	tokenID := c.GetString("token_id")
	tokenExp := c.GetInt64("token_exp")

	// 1. Luôn xóa cookie phía client
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "access_token",
		Value:    "",
		MaxAge:   -1,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		MaxAge:   -1,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteNoneMode,
	})

	// 2. Redis bắt buộc để revoke token server-side
	if config.RedisClient == nil {
		log.Printf("[AUDIT] LOGOUT_PARTIAL user_id=%s ip=%s (Redis unavailable, cookies cleared)", userID, c.ClientIP())
		c.JSON(503, gin.H{
			"success": false,
			"error":   "AUTH_STATE_UNAVAILABLE",
			"message": "Đã xóa cookie nhưng không thể vô hiệu hóa phiên server. Vui lòng thử lại.",
		})
		return
	}

	// 3. Revoke access token
	if tokenID != "" {
		now := time.Now().Unix()
		if remaining := tokenExp - now; remaining > 0 {
			if setErr := config.RedisClient.Set(config.Ctx, "blacklist:"+tokenID, "logged_out", time.Duration(remaining+60)*time.Second).Err(); setErr != nil {
				log.Printf("[Auth] Logout: failed to blacklist access JTI %s: %v", tokenID, setErr)
				c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Server session revoke failed"})
				return
			}
		}
	}

	// 4. Revoke refresh token + xóa khỏi whitelist
	if refreshCookie, err := c.Cookie("refresh_token"); err == nil && refreshCookie != "" {
		if refreshClaims, err := utils.VerifyToken(refreshCookie, true); err == nil {
			if ttl := time.Until(refreshClaims.ExpiresAt.Time); ttl > 0 {
				if setErr := config.RedisClient.Set(config.Ctx, "blacklist:"+refreshClaims.ID, "logged_out", ttl+60*time.Second).Err(); setErr != nil {
					log.Printf("[Auth] Logout: failed to blacklist refresh JTI %s: %v", refreshClaims.ID, setErr)
					c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Server session revoke failed"})
					return
				}
			}
			if delErr := config.RedisClient.Del(config.Ctx, "refresh_active:"+refreshClaims.ID).Err(); delErr != nil {
				log.Printf("[Auth] Logout: failed to delete refresh JTI %s: %v", refreshClaims.ID, delErr)
				c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Server session revoke failed"})
				return
			}
		}
	}

	// 5. Xóa legacy session key
	if userID != "" {
		if delErr := config.RedisClient.Del(config.Ctx, fmt.Sprintf("session:%s", userID)).Err(); delErr != nil {
			log.Printf("[Auth] Logout: failed to delete legacy session for user %s: %v", userID, delErr)
			c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Server session revoke failed"})
			return
		}
	}
	log.Printf("[AUDIT] LOGOUT user_id=%s ip=%s", userID, c.ClientIP())

	c.JSON(200, gin.H{"success": true, "message": "Đã đăng xuất và vô hiệu hóa phiên làm việc"})
}

type GoogleLoginReq struct {
	Token  string `json:"token" binding:"required"`
	Intent string `json:"intent"` // "login" hoặc "register"
}

func GoogleLogin(c *gin.Context) {
	var req GoogleLoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Thiếu token xác thực"})
		return
	}

	// 1. Thử xác thực ID Token với Google trước
	if config.RedisClient == nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Authentication service unavailable"})
		return
	}

	clientID := config.Env.GoogleClientID
	if clientID == "" {
		log.Printf("[Auth] GoogleLogin: GOOGLE_CLIENT_ID not configured")
		c.JSON(500, gin.H{"success": false, "error": "CONFIG_ERROR", "message": "Dịch vụ đăng nhập Google chưa được cấu hình"})
		return
	}

	var email, name, googleID, avatarURL string
	payload, err := idtoken.Validate(config.Ctx, req.Token, clientID)

	if err != nil {
		// FALLBACK: Thử coi req.Token là Access Token và gọi UserInfo API
		userInfoReq, httpErr := http.NewRequestWithContext(config.Ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v3/userinfo", nil)
		if httpErr != nil {
			c.JSON(401, gin.H{"success": false, "error": "INVALID_TOKEN", "message": "Token Google không hợp lệ hoặc đã hết hạn"})
			return
		}
		userInfoReq.Header.Set("Authorization", "Bearer "+req.Token)
		resp, httpErr := http.DefaultClient.Do(userInfoReq)
		if httpErr != nil {
			c.JSON(401, gin.H{"success": false, "error": "INVALID_TOKEN", "message": "Token Google không hợp lệ hoặc đã hết hạn"})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			c.JSON(401, gin.H{"success": false, "error": "INVALID_TOKEN", "message": "Token Google không hợp lệ hoặc đã hết hạn"})
			return
		}
		body, _ := ioutil.ReadAll(resp.Body)
		var userInfo map[string]interface{}
		if err := json.Unmarshal(body, &userInfo); err != nil {
			c.JSON(500, gin.H{"success": false, "message": "Lỗi xử lý dữ liệu Google"})
			return
		}

		email, _ = userInfo["email"].(string)
		name, _ = userInfo["name"].(string)
		googleID, _ = userInfo["sub"].(string)

		if email == "" || googleID == "" {
			fmt.Printf("[Google Auth Error] Missing required fields: email=%s, sub=%s\n", email, googleID)
			c.JSON(401, gin.H{"success": false, "error": "INVALID_TOKEN", "message": "Thông tin từ Google không đầy đủ"})
			return
		}

		if picture, ok := userInfo["picture"].(string); ok {
			avatarURL = picture
		}
	} else {
		// SUCCESS: Dùng thông tin từ ID Token
		email = payload.Claims["email"].(string)
		name = payload.Claims["name"].(string)
		googleID = payload.Subject
		if picture, ok := payload.Claims["picture"].(string); ok {
			avatarURL = picture
		}
	}

	// 2. Tìm user trong DB
	var user models.User
	createdUser := false
	err = config.DB.QueryRow(config.Ctx, `
		SELECT id, email, name, COALESCE(google_id, ''), COALESCE(role, 'user'), persona, persona_set, COALESCE(bio, ''), COALESCE(urls, '[]'), COALESCE(avatar_url, '')
		FROM users WHERE email = $1`,
		email,
	).Scan(&user.ID, &user.Email, &user.Name, &user.GoogleID, &user.Role, &user.Persona, &user.PersonaSet, &user.Bio, &user.URLs, &user.AvatarURL)

	if err != nil {
		// TRƯỜNG HỢP: User chưa tồn tại -> Đăng ký mới
		personaVal := "student"
		personaSet := false
		randomPass := fmt.Sprintf("GOOGLE_AUTH_%d", time.Now().UnixNano())
		hashed, err := bcrypt.GenerateFromPassword([]byte(randomPass), 12)
		if err != nil {
			c.JSON(500, gin.H{"success": false, "message": "Không thể tạo tài khoản từ Google"})
			return
		}

		err = config.DB.QueryRow(config.Ctx, `
			INSERT INTO users (email, name, google_id, password_hash, persona, persona_set, avatar_url)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			email, name, googleID, string(hashed), personaVal, personaSet, avatarURL,
		).Scan(&user.ID)

		if err != nil {
			c.JSON(500, gin.H{"success": false, "message": "Không thể tạo tài khoản từ Google"})
			return
		}
		user.Email = email
		user.Name = name
		user.Role = "user"
		user.Persona = personaVal
		user.PersonaSet = personaSet
		user.AvatarURL = avatarURL
		createdUser = true
	} else {
		// TRƯỜNG HỢP: User đã tồn tại
		if user.GoogleID == "" {
			// Nếu người dùng đang ở trang REGISTER mà email đã tồn tại -> Báo lỗi
			if req.Intent == "register" {
				c.JSON(409, gin.H{
					"success": false,
					"error":   "ACCOUNT_EXISTS",
					"message": "Tài khoản đã có, vui lòng đăng nhập",
				})
				return
			}
			// Nếu người dùng đang ở trang LOGIN -> Tự động liên kết và cho qua
			if _, err := config.DB.Exec(config.Ctx, "UPDATE users SET google_id = $1 WHERE id = $2", googleID, user.ID); err != nil {
				c.JSON(500, gin.H{"success": false, "message": "Failed to link Google account"})
				return
			}
			user.GoogleID = googleID
		} else if user.GoogleID != googleID {
			// Cập nhật Google ID (nếu thay đổi sub của google - hiếm gặp)
			if _, err := config.DB.Exec(config.Ctx, "UPDATE users SET google_id = $1 WHERE id = $2", googleID, user.ID); err != nil {
				c.JSON(500, gin.H{"success": false, "message": "Failed to update Google account"})
				return
			}
		}
	}

	// 3. Tạo JWT & Set Cookie
	if config.RedisClient == nil {
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}
	access, refresh, refreshJTI, err := utils.GenerateTokenPair(user.ID, user.Role, user.Persona, true)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "error": "TOKEN_GENERATION_FAILED", "message": "Không thể tạo phiên đăng nhập"})
		return
	}
	if setErr := config.RedisClient.Set(config.Ctx, "refresh_active:"+refreshJTI, user.ID, 10*24*time.Hour).Err(); setErr != nil {
		log.Printf("[Auth] GoogleLogin: failed to store refresh JTI: %v", setErr)
		if createdUser {
			if _, delErr := config.DB.Exec(config.Ctx, "DELETE FROM users WHERE id = $1", user.ID); delErr != nil {
				log.Printf("[Auth] GoogleLogin: failed to rollback user %s after refresh JTI store failure: %v", user.ID, delErr)
			}
		}
		c.JSON(503, gin.H{"success": false, "error": "AUTH_STATE_UNAVAILABLE", "message": "Dịch vụ xác thực tạm thời không khả dụng"})
		return
	}
	log.Printf("[AUDIT] GOOGLE_LOGIN user_id=%s email=%s ip=%s", user.ID, user.Email, c.ClientIP())
	setTokenCookies(c, access, refresh, true)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"access_token":  access,
			"refresh_token": refresh,
			"user": gin.H{
				"id":         user.ID,
				"name":       user.Name,
				"email":      user.Email,
				"role":       user.Role,
				"persona":    user.Persona,
				"bio":        user.Bio,
				"urls":       user.URLs,
				"avatar_url": user.AvatarURL,
			},
		},
	})
}
func CheckEmail(c *gin.Context) {
	email := c.Query("email")
	if email == "" {
		c.JSON(400, gin.H{"success": false, "message": "Thiếu email"})
		return
	}

	// Trả response thống nhất — không leak thông tin email có tồn tại hay không.
	// /auth/register sẽ trả 409 EMAIL_ALREADY_EXISTS nếu submit với email đã dùng.
	c.JSON(200, gin.H{"success": true, "message": "Nếu email hợp lệ, bạn có thể tiếp tục đăng ký."})
}
