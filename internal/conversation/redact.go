package conversation

import (
	"encoding/json"
	"regexp"
	"strings"
)

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password)\s*[:=]\s*)[^\s,;]+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password)["']?\s*:\s*["'])[^"']+`),
	regexp.MustCompile(`\b\d{8,12}:[A-Za-z0-9_-]{25,}\b`),
}

func sensitiveKey(k string) bool {
	k = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(k, "-", ""), "_", ""))
	switch k {
	case "authorization", "apikey", "token", "accesstoken", "refreshtoken", "bottoken", "secret", "clientsecret", "password":
		return true
	default:
		return false
	}
}

func redactValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, value := range x {
			if sensitiveKey(k) {
				x[k] = "[REDACTED]"
			} else {
				x[k] = redactValue(value)
			}
		}
	case []any:
		for i := range x {
			x[i] = redactValue(x[i])
		}
	case string:
		return Redact(x)
	}
	return v
}

// RedactJSON structurally removes credential-valued fields at any depth and
// applies free-text redaction to remaining strings.
func RedactJSON(raw json.RawMessage) json.RawMessage {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return json.RawMessage(`{"redacted":"invalid payload"}`)
	}
	clean, err := json.Marshal(redactValue(value))
	if err != nil {
		return json.RawMessage(`{"redacted":"invalid payload"}`)
	}
	return clean
}

// Redact replaces common credential shapes before text reaches persistent
// storage. It is defense in depth; callers must still avoid passing secrets.
func Redact(s string) string {
	for _, pattern := range sensitivePatterns {
		s = pattern.ReplaceAllString(s, `${1}[REDACTED]`)
	}
	return s
}
