package config

import (
	"net/url"
	"strings"
)

func normalizeOrigin(origin string) string {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		return ""
	}

	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	if strings.TrimRight(parsed.Path, "/") != "" {
		return ""
	}

	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
}

func IsAllowedOrigin(origin string) bool {
	normalizedOrigin := normalizeOrigin(origin)
	if normalizedOrigin == "" {
		return false
	}

	for _, allowed := range Env.CORSOrigins {
		if normalizedOrigin == normalizeOrigin(allowed) {
			return true
		}
	}
	return false
}
