package controllers

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"log"
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

	rand.Seed(time.Now().UnixNano())
	// Ensure positive int64 for OrderCode less than 9007199254740991 (js max safe integer)
	orderCode := time.Now().Unix()*10 + int64(rand.Intn(10))

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
		config.DB.Exec(config.Ctx, "UPDATE payments SET status = 'PAID', updated_at = NOW() WHERE order_code = $1", orderCode)
		config.DB.Exec(config.Ctx, "UPDATE users SET tier = $1 WHERE id = $2", pkg, userID)
		
		if config.RedisClient != nil {
			config.RedisClient.Del(config.Ctx, fmt.Sprintf("user:profile:%s", userID))
		}
		c.JSON(200, gin.H{"success": true, "message": "Xác nhận nâng cấp thành công!"})
		return
	}

	c.JSON(400, gin.H{"success": false, "message": "Giao dịch chưa hoàn tất", "status": info.Status})
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
