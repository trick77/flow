package contractgen

import (
	"strings"
	"unicode"
)

// initialisms are the words Go spells in capitals (ToolID, not ToolId).
var initialisms = map[string]string{
	"id": "ID", "url": "URL", "sha": "SHA", "api": "API", "usd": "USD", "json": "JSON", "http": "HTTP",
	"mcp": "MCP", "ui": "UI", "ip": "IP", "cpu": "CPU", "ssh": "SSH", "oidc": "OIDC", "ts": "TS", "uri": "URI",
}

// words splits camelCase, snake_case, dotted and dashed names into lower-case words.
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r == '_' || r == '.' || r == '-' || r == ' ' || r == '/':
			flush()
		case unicode.IsUpper(r):
			// A new word starts at an upper-case letter, unless it continues a run of capitals (an initialism) that
			// is not followed by a lower-case letter.
			if len(cur) > 0 && (!unicode.IsUpper(cur[len(cur)-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]))) {
				flush()
			}
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}

// goName is the exported Go identifier for a JSON name or an enum value.
func goName(s string) string {
	var b strings.Builder
	for _, w := range words(s) {
		if up, ok := initialisms[w]; ok {
			b.WriteString(up)
			continue
		}
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}

// constName is the SCREAMING_SNAKE name of a TypeScript constant.
func constName(s string) string {
	return strings.ToUpper(strings.Join(words(s), "_"))
}
