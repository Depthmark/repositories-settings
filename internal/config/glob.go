package config

import (
	"regexp"
	"strings"
	"sync"
)

var (
	globCache   = make(map[string]*regexp.Regexp)
	globCacheMu sync.Mutex
)

// globRegex compiles a glob pattern into a case-insensitive anchored
// regexp. Mirrors the TS implementation in src/core/policy-engine.ts.
func globRegex(pattern string) *regexp.Regexp {
	globCacheMu.Lock()
	defer globCacheMu.Unlock()
	if re, ok := globCache[pattern]; ok {
		return re
	}
	const globstar = "\x00\x00GLOBSTAR\x00\x00"
	p := pattern
	// Escape regex metacharacters except *, ?.
	p = regexpEscape(p)
	p = strings.ReplaceAll(p, "**", globstar)
	p = strings.ReplaceAll(p, "*", "[^/]*")
	p = strings.ReplaceAll(p, globstar, ".*")
	p = strings.ReplaceAll(p, `\?`, "[^/]")
	re := regexp.MustCompile("(?i)^" + p + "$")
	globCache[pattern] = re
	return re
}

func regexpEscape(s string) string {
	const meta = `.+^${}()|[]\`
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(meta, r) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
