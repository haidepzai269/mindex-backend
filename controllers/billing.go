package controllers

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/payOSHQ/payos-lib-golang"
	"mindex-backend/config"
)

// -- Utils
func setupPayOS() {
	clientID := os.Getenv("PAYOS_CLIENT_ID")
	apiKey := os.Getenv("PAYOS_API_KEY")
	checksumKey := os.Getenv("PAYOS_CHECKSUM_KEY")
	_ = payos.Key(clientID, apiKey, checksumKey)
}

func getSystemSetting(key string, defaultVal string) string {
	var val string
	err := config.DB.QueryRow(config.Ctx, "SELECT value FROM system_settings WHERE key = $1", key).Scan(&val)
	if err != nil {
		return defaultVal
	}
	return val
}

// ---------------- USER ----------------

func GetPackages(c *gin.Context) {
	proPrice, _ := strconv.Atoi(getSystemSetting("PRO_PRICE", "5000"))
	ultraPrice, _ := strconv.Atoi(getSystemSetting("ULTRA_PRICE", "10000"))

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"PRO":   proPrice,
			"ULTRA": ultraPrice,
		},
	})
}

type CreateLinkReq struct {
	PackageName string `json:"package_name" binding:"required"` // PRO | ULTRA
}

func CreatePaymentLink(c *gin.Context) {
	setupPayOS()

	userID := c.GetString("user_id")
	var req CreateLinkReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Tham số package_name không hợp lệ"})
		return
	}

	var amount int
	if req.PackageName == "PRO" {
		amount, _ = strconv.Atoi(getSystemSetting("PRO_PRICE", "5000"))
	} else if req.PackageName == "ULTRA" {
		amount, _ = strconv.Atoi(getSystemSetting("ULTRA_PRICE", "10000"))
	} else {
		c.JSON(400, gin.H{"success": false, "message": "Package không tồn tại"})
		return
	}

	// Tạo orderCode an toàn, tránh rand.Seed deprecated
	n, _ := rand.Int(rand.Reader, big.NewInt(10))
	orderCode := time.Now().Unix()*10 + n.Int64()

	_, err := config.DB.Exec(config.Ctx, 
		"INSERT INTO payments (user_id, order_code, amount, package_name, status) VALUES ($1, $2, $3, $4, 'PENDING')",
		userID, orderCode, amount, req.PackageName,
	)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Lỗi lưu giao dịch"})
		return
	}

	origin := c.Request.Header.Get("Origin")
	if origin == "" {
		origin = "http://localhost:3000"
	}
	returnUrl := fmt.Sprintf("%s/settings/billings?status=success&orderCode=%d", origin, orderCode)
	cancelUrl := fmt.Sprintf("%s/settings/billings?status=cancel", origin)

	checkoutReq := payos.CheckoutRequestType{
		OrderCode:   orderCode,
		Amount:      amount,
		Description: req.PackageName,
		Items:       []payos.Item{{Name: "Upgrade " + req.PackageName, Quantity: 1, Price: amount}},
		ReturnUrl:   returnUrl,
		CancelUrl:   cancelUrl,
	}

	res, errPayOS := payos.CreatePaymentLink(checkoutReq)
	if errPayOS != nil {
		log.Println("Error create PayOS: ", errPayOS)
		c.JSON(500, gin.H{"success": false, "message": "Lỗi khởi tạo PayOS payment"})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"checkout_url": res.CheckoutUrl,
			"order_code":   orderCode,
		},
	})
}

func VerifyPaymentClient(c *gin.Context) {
	setupPayOS()

	userID := c.GetString("user_id")
	orderCodeStr := c.Query("orderCode")
	orderCode, _ := strconv.ParseInt(orderCodeStr, 10, 64)

	var pkg string
	var status string
	err := config.DB.QueryRow(config.Ctx, "SELECT package_name, status FROM payments WHERE order_code = $1 AND user_id = $2", orderCode, userID).Scan(&pkg, &status)
	
	if err != nil {
		c.JSON(404, gin.H{"success": false, "message": "Giao dịch không hợp lệ"})
		return
	}

	if status == "PAID" {
		c.JSON(200, gin.H{"success": true, "message": "Bạn đã được nâng cấp!"})
		return
	}

	info, err := payos.GetPaymentLinkInformation(orderCodeStr)
	
	if err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Không thể lấy thông tin từ PayOS"})
		return
	}

	if info.Status == "PAID" {
		expiresAt := time.Now().AddDate(0, 1, 0) // 1 tháng
		config.DB.Exec(config.Ctx, "UPDATE payments SET status = 'PAID', updated_at = NOW() WHERE order_code = $1", orderCode)
		config.DB.Exec(config.Ctx, "UPDATE users SET tier = $1, tier_expires_at = $2 WHERE id = $3", pkg, expiresAt, userID)

		if config.RedisClient != nil {
			config.RedisClient.Del(config.Ctx, fmt.Sprintf("user:profile:%s", userID))
		}
		c.JSON(200, gin.H{"success": true, "message": "Xác nhận nâng cấp thành công!"})
		return
	}

	c.JSON(400, gin.H{"success": false, "message": "Giao dịch chưa hoàn tất", "status": info.Status})
}

// verifyPayOSSignature xác thực chữ ký HMAC-SHA256 từ PayOS webhook
func verifyPayOSSignature(data map[string]interface{}, signature string) bool {
	checksumKey := os.Getenv("PAYOS_CHECKSUM_KEY")
	if checksumKey == "" {
		return false
	}
	// Sắp xếp keys theo thứ tự alphabet, nối thành "key=value&key=value"
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, data[k]))
	}
	payload := strings.Join(parts, "&")

	mac := hmac.New(sha256.New, []byte(checksumKey))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// PayOSWebhook nhận callback từ PayOS sau khi thanh toán thành công
// POST /api/v1/billings/webhook (no auth — public, verified by checksum)
func PayOSWebhook(c *gin.Context) {
	var body struct {
		Code      string                 `json:"code"`
		Data      map[string]interface{} `json:"data"`
		Signature string                 `json:"signature"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	// Verify HMAC-SHA256 signature
	if !verifyPayOSSignature(body.Data, body.Signature) {
		log.Printf("[PayOS Webhook] Invalid signature")
		c.JSON(400, gin.H{"error": "invalid signature"})
		return
	}

	// Chỉ xử lý thanh toán thành công
	if body.Code != "00" {
		c.JSON(200, gin.H{"message": "ok"})
		return
	}

	orderCodeFloat, _ := body.Data["orderCode"].(float64)
	orderCode := int64(orderCodeFloat)
	if orderCode == 0 {
		c.JSON(200, gin.H{"message": "ok"})
		return
	}

	var userID, pkg, status string
	var dbAmount int
	err := config.DB.QueryRow(config.Ctx,
		"SELECT user_id, package_name, status, amount FROM payments WHERE order_code = $1",
		orderCode).Scan(&userID, &pkg, &status, &dbAmount)
	if err != nil || status == "PAID" {
		c.JSON(200, gin.H{"message": "ok"})
		return
	}

	// Verify amount từ webhook khớp với DB để chống tampering
	webhookAmountFloat, _ := body.Data["amount"].(float64)
	webhookAmount := int(webhookAmountFloat)
	if webhookAmount > 0 && webhookAmount != dbAmount {
		log.Printf("[PayOS Webhook] ⚠️ Amount mismatch! orderCode=%d db=%d webhook=%d user=%s", orderCode, dbAmount, webhookAmount, userID)
		c.JSON(400, gin.H{"error": "amount mismatch"})
		return
	}

	expiresAt := time.Now().AddDate(0, 1, 0) // 1 tháng
	config.DB.Exec(config.Ctx, "UPDATE payments SET status = 'PAID', updated_at = NOW() WHERE order_code = $1", orderCode)
	config.DB.Exec(config.Ctx, "UPDATE users SET tier = $1, tier_expires_at = $2 WHERE id = $3", pkg, expiresAt, userID)

	if config.RedisClient != nil {
		config.RedisClient.Del(config.Ctx, fmt.Sprintf("user:profile:%s", userID))
	}

	log.Printf("[PayOS Webhook] ✅ User %s upgraded to %s (expires %s) via orderCode %d", userID, pkg, expiresAt.Format("2006-01-02"), orderCode)
	c.JSON(200, gin.H{"message": "ok"})
}

// GetPaymentHistory — GET /api/v1/billings/history
// Lịch sử giao dịch của user hiện tại
func GetPaymentHistory(c *gin.Context) {
	userID := c.GetString("user_id")

	rows, err := config.DB.Query(config.Ctx, `
		SELECT order_code, amount, package_name, status, created_at
		FROM payments WHERE user_id = $1
		ORDER BY created_at DESC LIMIT 20`, userID)
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Lỗi truy vấn"})
		return
	}
	defer rows.Close()

	type Payment struct {
		OrderCode   int64     `json:"order_code"`
		Amount      int       `json:"amount"`
		PackageName string    `json:"package_name"`
		Status      string    `json:"status"`
		CreatedAt   time.Time `json:"created_at"`
	}
	var history []Payment
	for rows.Next() {
		var p Payment
		rows.Scan(&p.OrderCode, &p.Amount, &p.PackageName, &p.Status, &p.CreatedAt)
		history = append(history, p)
	}
	if history == nil {
		history = []Payment{}
	}
	c.JSON(200, gin.H{"success": true, "data": history})
}

// ---------------- ADMIN ----------------

func AdminGetBillingsStats(c *gin.Context) {
	proPrice, _ := strconv.Atoi(getSystemSetting("PRO_PRICE", "5000"))
	ultraPrice, _ := strconv.Atoi(getSystemSetting("ULTRA_PRICE", "10000"))

	rows, _ := config.DB.Query(config.Ctx, `
		SELECT p.id, u.name, u.email, p.order_code, p.amount, p.package_name, p.status, p.created_at
		FROM payments p
		JOIN users u ON u.id = p.user_id
		ORDER BY p.created_at DESC LIMIT 100
	`)
	defer rows.Close()

	var payments []gin.H
	for rows.Next() {
		var id, userName, email, pkgName, status string
		var orderCode int64
		var amount int
		var created time.Time
		rows.Scan(&id, &userName, &email, &orderCode, &amount, &pkgName, &status, &created)
		payments = append(payments, gin.H{
			"id":           id,
			"user_name":    userName,
			"user_email":   email,
			"order_code":   orderCode,
			"amount":       amount,
			"package_name": pkgName,
			"status":       status,
			"created_at":   created,
		})
	}
	if payments == nil {
		payments = []gin.H{}
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"packages": gin.H{"PRO": proPrice, "ULTRA": ultraPrice},
			"payments": payments,
		},
	})
}

type UpdatePriceReq struct {
	ProPrice   int `json:"pro_price"`
	UltraPrice int `json:"ultra_price"`
}

func AdminUpdateBillingPrices(c *gin.Context) {
	var req UpdatePriceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Tham số không hợp lệ"})
		return
	}

	config.DB.Exec(config.Ctx, "INSERT INTO system_settings (key, value) VALUES ('PRO_PRICE', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", strconv.Itoa(req.ProPrice))
	config.DB.Exec(config.Ctx, "INSERT INTO system_settings (key, value) VALUES ('ULTRA_PRICE', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", strconv.Itoa(req.UltraPrice))

	c.JSON(200, gin.H{"success": true, "message": "Cập nhật bảng giá thành công"})
}
