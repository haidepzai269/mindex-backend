package utils

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var sensitiveWords = []string{
	// 1. Nhóm từ tục tĩu tiếng Việt
	"dit", "du", "deo", "dech", "dm", "dme", "ditme", "dume", "vailon", "vcl", "vl",
	"lon", "cac", "cu", "buoi", "buom", "chim", "chochet", "sucvat", "khonnan", "ngunhucho",
	// 2. Nhóm từ xúc phạm
	"ngu", "dan", "occho", "oclon", "sucsinh", "thangcho", "concho", "racruoi", "thatbai", "dongu", "nguhoc",
	// 3. Nhóm từ nhạy cảm tình dục
	"sex", "sextoy", "porn", "pornhub", "xxx", "18", "jav", "hentai", "nude", "naked", "onlyfans", "webcamsex",
	// 4. Nhóm từ bạo lực
	"giet", "gietnguoi", "amsat", "chem", "dam", "tratan", "khungbo", "bom", "thuocno",
	// 5. Nhóm từ ma túy
	"matuy", "heroin", "cocaine", "ketamine", "comy", "cansa", "weed", "meth", "ecstasy", "lsd",
	// 6. Nhóm từ tục tĩu tiếng Anh
	"fuck", "fucking", "motherfucker", "bitch", "bastard", "asshole", "dick", "cock", "pussy", "cunt", "slut", "whore",
}

// NormalizeSensitiveText chuẩn hóa text để phát hiện từ lóng, chửi thề
// Giữ lại chỉ chữ cái (a-z) và số (0-9), loại bỏ khoảng trắng và ký tự đặc biệt
func NormalizeSensitiveText(text string) string {
	text = strings.ToLower(text)
	text = strings.ReplaceAll(text, "đ", "d")
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	text, _, _ = transform.String(t, text)

	var sb strings.Builder
	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// IsSensitiveContent kiểm tra xem text có chứa từ nhạy cảm không
func IsSensitiveContent(text string) bool {
	normText := NormalizeSensitiveText(text)
	for _, w := range sensitiveWords {
		if strings.Contains(normText, w) {
			return true
		}
	}
	return false
}
