package controllers

import (
	"encoding/json"
	"fmt"
	"mindex-backend/config"
	"mindex-backend/internal/persona"
	"mindex-backend/models"
	"mindex-backend/utils"
	"strings"
	"time"

	"crypto/rand"
	"math/big"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/api/idtoken"
)

func generateOTP() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(900000))
	return fmt.Sprintf("%06d", n.Int64()+100000)
}

func setTokenCookies(c *gin.Context, access, refresh string) {
	// Access Token: 1 hour (3600 seconds)
	c.SetCookie("access_token", access, 3600, "/", "", false, true)
	// Refresh Token: 7 days (604800 seconds)
	if refresh != "" {
		c.SetCookie("refresh_token", refresh, 604800, "/", "", false, true)
	}
}

type RegisterReq struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
	Name     string `json:"name" binding:"required"`
	Persona  string `json:"persona"` // Optional
}

type LoginReq struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func Register(c *gin.Context) {
	var req RegisterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": "VALIDATION_ERROR", "message": "Email sai định dạng hoặc password < 8 ký tự"})
		return
	}

	hashed, _ := bcrypt.GenerateFromPassword([]byte(req.Password), 12)

	personaVal := req.Persona
	personaSet := true
	if personaVal == "" {
		personaVal = "student"
		personaSet = false
	}

	var userID string
	err := config.DB.QueryRow(
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

	access, refresh, _ := utils.GenerateTokenPair(userID, "user", personaVal)

	setTokenCookies(c, access, refresh)

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
		c.JSON(401, gin.H{"success": false, "error": "INVALID_CREDENTIALS", "message": "Sai email hoặc mật khẩu"})
		return
	}

	access, refresh, _ := utils.GenerateTokenPair(user.ID, user.Role, user.Persona)

	setTokenCookies(c, access, refresh)

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

	access, _, _ := utils.GenerateTokenPair(claims.UserID, claims.Role, claims.Persona)

	// Cập nhật access token mới vào cookie
	c.SetCookie("access_token", access, 3600, "/", "", false, true)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"access_token": access,
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

	// 1. Kiểm tra OTP từ Redis
	if config.RedisClient != nil {
		cacheKey := fmt.Sprintf("otp:password:%s", userID)
		storedOTP, err := config.RedisClient.Get(config.Ctx, cacheKey).Result()
		if err != nil || storedOTP != req.OTPCode {
			c.JSON(400, gin.H{"success": false, "error": "INVALID_OTP", "message": "Mã xác thực không chính xác hoặc đã hết hạn"})
			return
		}
		// Xóa OTP sau khi dùng
		config.RedisClient.Del(config.Ctx, cacheKey)
	}

	var hashedOld string
	err := config.DB.QueryRow(config.Ctx, "SELECT password_hash FROM users WHERE id = $1", userID).Scan(&hashedOld)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Lỗi hệ thống"})
		return
	}

	if bcrypt.CompareHashAndPassword([]byte(hashedOld), []byte(req.OldPassword)) != nil {
		c.JSON(401, gin.H{"success": false, "error": "INVALID_OLD_PASSWORD", "message": "Mật khẩu cũ không chính xác"})
		return
	}

	hashedNew, _ := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	_, err = config.DB.Exec(config.Ctx, "UPDATE users SET password_hash = $1 WHERE id = $2", string(hashedNew), userID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể cập nhật mật khẩu"})
		return
	}

	// Xóa cache (mặc dù chỉ chứa profile nhưng cẩn thận vẫn tốt)
	if config.RedisClient != nil {
		config.RedisClient.Del(config.Ctx, fmt.Sprintf("user:profile:%s", userID))
	}

	c.JSON(200, gin.H{"success": true, "message": "Đã đổi mật khẩu thành công"})
}

// SendPasswordOTP gửi mã OTP cho người dùng đang đăng nhập để đổi mật khẩu
func SendPasswordOTP(c *gin.Context) {
	userID := c.GetString("user_id")

	// Lấy email của user
	var email string
	err := config.DB.QueryRow(config.Ctx, "SELECT email FROM users WHERE id = $1", userID).Scan(&email)
	if err != nil {
		c.JSON(404, gin.H{"success": false, "message": "Không tìm thấy người dùng"})
		return
	}

	otp := generateOTP()

	// Lưu vào Redis (5 phút)
	if config.RedisClient != nil {
		cacheKey := fmt.Sprintf("otp:password:%s", userID)
		config.RedisClient.Set(config.Ctx, cacheKey, otp, 5*time.Minute)
	}

	// Gửi Email
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

	// Kiểm tra email tồn tại
	var userID string
	err := config.DB.QueryRow(config.Ctx, "SELECT id FROM users WHERE email = $1", req.Email).Scan(&userID)
	if err != nil {
		// Để bảo mật, không báo là email không tồn tại, chỉ báo thành công giả định nếu muốn, 
		// nhưng ở đây có thể báo lỗi cho tiện UX.
		c.JSON(404, gin.H{"success": false, "message": "Email không tồn tại trong hệ thống"})
		return
	}

	otp := generateOTP()

	// Lưu vào Redis theo Email (5 phút)
	if config.RedisClient != nil {
		cacheKey := fmt.Sprintf("otp:reset:%s", req.Email)
		config.RedisClient.Set(config.Ctx, cacheKey, otp, 5*time.Minute)
	}

	// Gửi Email
	err = utils.SendOTPEmail(req.Email, otp, "Khôi phục mật khẩu")
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể gửi email xác thực"})
		return
	}

	c.JSON(200, gin.H{"success": true, "message": "Mã khôi phục đã được gửi tới email của bạn"})
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

	// 1. Kiểm tra OTP từ Redis
	if config.RedisClient != nil {
		cacheKey := fmt.Sprintf("otp:reset:%s", req.Email)
		storedOTP, err := config.RedisClient.Get(config.Ctx, cacheKey).Result()
		if err != nil || storedOTP != req.OTPCode {
			c.JSON(400, gin.H{"success": false, "message": "Mã xác thực không chính xác hoặc đã hết hạn"})
			return
		}
		// Xóa OTP
		config.RedisClient.Del(config.Ctx, cacheKey)
	}

	// 2. Hash mật khẩu mới
	hashed, _ := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)

	// 3. Cập nhật DB
	_, err := config.DB.Exec(config.Ctx, "UPDATE users SET password_hash = $1 WHERE email = $2", string(hashed), req.Email)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Không thể cập nhật mật khẩu"})
		return
	}

	c.JSON(200, gin.H{"success": true, "message": "Mật khẩu đã được đặt lại thành công"})
}

func Logout(c *gin.Context) {
	// Xóa cookie bằng cách set MaxAge = -1
	c.SetCookie("access_token", "", -1, "/", "", false, true)
	c.SetCookie("refresh_token", "", -1, "/", "", false, true)

	c.JSON(200, gin.H{
		"success": true,
		"message": "Đã đăng xuất",
	})
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

	// 1. Xác thực ID Token với Google
	clientID := config.Env.GoogleClientID
	if clientID == "" {
		clientID = "610711552000-nlnnovm60gf63bscps5tklsjbub8pdv6.apps.googleusercontent.com"
	}

	payload, err := idtoken.Validate(config.Ctx, req.Token, clientID)
	if err != nil {
		c.JSON(401, gin.H{"success": false, "error": "INVALID_TOKEN", "message": "Token Google không hợp lệ hoặc đã hết hạn"})
		return
	}

	email := payload.Claims["email"].(string)
	name := payload.Claims["name"].(string)
	googleID := payload.Subject
	avatarURL := ""
	if picture, ok := payload.Claims["picture"].(string); ok {
		avatarURL = picture
	}

	// 2. Tìm user trong DB
	var user models.User
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
		hashed, _ := bcrypt.GenerateFromPassword([]byte(randomPass), 12)

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
	} else {
		// TRƯỜNG HỢP: User đã tồn tại
		if user.GoogleID == "" {
			// Nếu người dùng đang ở trang REGISTER mà email đã tồn tại -> Báo lỗi
			if req.Intent == "register" {
				c.JSON(409, gin.H{
					"success": false, 
					"error": "ACCOUNT_EXISTS", 
					"message": "Tài khoản đã có, vui lòng đăng nhập",
				})
				return
			}
			// Nếu người dùng đang ở trang LOGIN -> Tự động liên kết và cho qua
			_, _ = config.DB.Exec(config.Ctx, "UPDATE users SET google_id = $1 WHERE id = $2", googleID, user.ID)
			user.GoogleID = googleID
		} else if user.GoogleID != googleID {
			// Cập nhật Google ID (nếu thay đổi sub của google - hiếm gặp)
			_, _ = config.DB.Exec(config.Ctx, "UPDATE users SET google_id = $1 WHERE id = $2", googleID, user.ID)
		}
	}

	// 3. Tạo JWT & Set Cookie
	access, refresh, _ := utils.GenerateTokenPair(user.ID, user.Role, user.Persona)
	setTokenCookies(c, access, refresh)

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
