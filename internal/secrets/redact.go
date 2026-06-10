package secrets

import "strings"

func Redact(input string, values map[string]string) string {
	out := input
	for name, value := range values {
		if value == "" {
			continue
		}
		out = strings.ReplaceAll(out, value, "[REDACTED:"+name+"]")
	}
	return out
}
