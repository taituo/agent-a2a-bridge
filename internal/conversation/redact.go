package conversation

import "regexp"

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password)\s*[:=]\s*)[^\s,;]+`),
	regexp.MustCompile(`\b\d{8,12}:[A-Za-z0-9_-]{25,}\b`),
}

// Redact replaces common credential shapes before text reaches persistent
// storage. It is defense in depth; callers must still avoid passing secrets.
func Redact(s string) string {
	for _, pattern := range sensitivePatterns {
		s = pattern.ReplaceAllString(s, `${1}[REDACTED]`)
	}
	return s
}
