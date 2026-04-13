package utils

import (
	"fmt"
	"log"
	"mindex-backend/config"
	"net/smtp"
)

// SendEmail gửi một email thông qua SMTP server cấu hình trong .env
func SendEmail(to, subject, body string) error {
	// Kiểm tra cấu hình
	if config.Env.SMTPHost == "" || config.Env.SMTPUser == "" {
		log.Println("⚠️ Cảnh báo: SMTP chưa được cấu hình. Không thể gửi email.")
		// Trong môi trường dev, chúng ta log ra mã OTP để test mà không cần mail server
		fmt.Printf("\n--- [DEV MAIL SIMULATION] ---\nTo: %s\nSubject: %s\nBody: %s\n-----------------------------\n\n", to, subject, body)
		return nil
	}

	auth := smtp.PlainAuth("", config.Env.SMTPUser, config.Env.SMTPPass, config.Env.SMTPHost)

	// Định dạng message email
	mime := "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\n\n"
	msg := []byte(fmt.Sprintf("From: %s\nTo: %s\nSubject: %s\n%s%s", config.Env.SMTPFrom, to, subject, mime, body))

	addr := fmt.Sprintf("%s:%s", config.Env.SMTPHost, config.Env.SMTPPort)

	err := smtp.SendMail(addr, auth, config.Env.SMTPUser, []string{to}, msg)
	if err != nil {
		log.Printf("❌ Lỗi gửi email: %v", err)
		return err
	}

	log.Printf("✅ Đã gửi email tới %s", to)
	return nil
}

// SendOTPEmail gửi mã OTP xác thực
func SendOTPEmail(to, code, action string) error {
	subject := fmt.Sprintf("[Mindex] Mã xác thực %s của bạn", action)
	body := fmt.Sprintf(`
		<div style="font-family: sans-serif; max-width: 600px; margin: 0 auto; padding: 20px; border: 1px solid #eee; border-radius: 10px;">
			<h2 style="color: #b829ff; text-align: center;">Xác thực Mindex</h2>
			<p>Chào bạn,</p>
			<p>Bạn vừa yêu cầu mã xác thực cho hành động: <strong>%s</strong>.</p>
			<div style="background: #f4f4f4; padding: 20px; text-align: center; border-radius: 5px; margin: 20px 0;">
				<span style="font-size: 32px; font-weight: bold; letter-spacing: 5px; color: #333;">%s</span>
			</div>
			<p>Mã này có hiệu lực trong <strong>5 phút</strong>. Vui lòng không chia sẻ mã này với bất kỳ ai.</p>
			<hr style="border: 0; border-top: 1px solid #eee; margin: 20px 0;">
			<p style="font-size: 12px; color: #999; text-align: center;">Đây là email tự động, vui lòng không trả lời.</p>
		</div>
	`, action, code)

	return SendEmail(to, subject, body)
}
