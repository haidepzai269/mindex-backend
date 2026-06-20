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

// hasVietnameseDiacritics trả về true nếu word chứa ký tự non-ASCII (tức là có dấu tiếng Việt).
// Dùng để phân biệt "dự" (có nghĩa, có dấu) với "du" (tục tĩu, không dấu).
func hasVietnameseDiacritics(word string) bool {
	for _, r := range word {
		if r > 127 {
			return true
		}
	}
	return false
}

// IsSensitiveContent kiểm tra xem text có chứa từ nhạy cảm không.
//
// Hai bước:
//  1. Per-word: kiểm tra từng token riêng. Từ ngắn (<=3 ký tự) chỉ exact-match để tránh
//     false positive kiểu "dự" → "du", "cự" → "cu". Nếu từ gốc có dấu tiếng Việt thì bỏ qua.
//  2. Anti-evasion: collapse toàn bộ (xóa khoảng trắng) để bắt "f u c k", "d.i.t.m.e".
//     Chỉ áp dụng cho từ nhạy cảm dài >= 4 ký tự.
func IsSensitiveContent(text string) bool {
	// Bước 1: per-word check
	for _, word := range strings.Fields(text) {
		normWord := NormalizeSensitiveText(word)
		for _, sw := range sensitiveWords {
			if len(sw) <= 3 {
				// Exact match; nếu từ gốc có dấu tiếng Việt → không phải tục tĩu
				if normWord == sw && !hasVietnameseDiacritics(word) {
					return true
				}
			} else {
				if strings.Contains(normWord, sw) {
					return true
				}
			}
		}
	}

	// Bước 2: anti-evasion (chỉ từ >= 4 ký tự để không bắt nhầm "du" trong "duan")
	collapsed := NormalizeSensitiveText(text)
	for _, sw := range sensitiveWords {
		if len(sw) >= 4 && strings.Contains(collapsed, sw) {
			return true
		}
	}

	return false
}
