package executor

import "strings"

// sameModelIdentity reports whether a requested model name and the model that
// was actually sent upstream denote the same model.
//
// Provider aliases usually only add a routing segment: an Ollama Cloud account
// exposing "deepseek-v4-flash:0731" as "ollama/deepseek-v4-flash:0731" makes the
// executor send the unprefixed name upstream while the request log keeps the
// prefixed one. Recording that pair as "requested X, upstream Y" turns every
// aliased request into a bogus "real model ID" hint in the console, so treat a
// pure routing-prefix difference as the same model. Aliases that rename the
// model (for example "fast" -> "claude-sonnet-4") stay different and are still
// reported, because there the upstream ID carries real information.
func sameModelIdentity(requested, upstream string) bool {
	a := strings.ToLower(strings.TrimSpace(requested))
	b := strings.ToLower(strings.TrimSpace(upstream))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a)
}
