// Package tlsfingerprint gives upstream requests a configurable TLS ClientHello
// fingerprint.
//
// Go's crypto/tls emits a ClientHello that no shipping browser or CLI produces,
// so JA3/JA4 based filtering can single out proxy traffic before a single byte
// of the request is seen. Sending a ClientHello that matches a real client
// removes that signal.
//
// Profiles are deliberately named after real clients rather than raw cipher
// lists: the point is to match a client that exists in the wild, and utls keeps
// the underlying byte sequences current as those clients ship new versions.
package tlsfingerprint

import (
	"sort"
	"strings"

	utls "github.com/refraction-networking/utls"
)

// Profile names accepted by configuration. These are the stable identifiers
// exposed to operators; the utls ClientHelloID behind each one may advance as
// the library tracks new browser releases.
const (
	ProfileChrome     = "chrome"
	ProfileFirefox    = "firefox"
	ProfileSafari     = "safari"
	ProfileEdge       = "edge"
	ProfileIOS        = "ios"
	ProfileAndroid    = "android"
	ProfileRandomized = "randomized"
)

// DefaultProfile is used when TLS fingerprinting is enabled without naming a
// profile. Chrome is the most common ClientHello on the public internet, so it
// is the least remarkable choice.
const DefaultProfile = ProfileChrome

// profiles maps configuration names to utls ClientHello templates.
//
// The _Auto variants track the newest version utls implements, which is what
// keeps a profile from ageing into a fingerprint of its own once the real
// client has moved on.
var profiles = map[string]utls.ClientHelloID{
	ProfileChrome:  utls.HelloChrome_Auto,
	ProfileFirefox: utls.HelloFirefox_Auto,
	ProfileSafari:  utls.HelloSafari_Auto,
	ProfileEdge:    utls.HelloEdge_Auto,
	ProfileIOS:     utls.HelloIOS_Auto,
	ProfileAndroid: utls.HelloAndroid_11_OkHttp,
	// Randomized generates a fresh, internally consistent ClientHello per
	// connection. It defeats fingerprint matching but is itself unusual, since
	// no real client varies its ClientHello between connections.
	ProfileRandomized: utls.HelloRandomizedALPN,
}

// ResolveProfile maps a configured profile name to its ClientHello template.
// It reports false for unknown names so callers can fail loudly at config load
// rather than silently falling back to a fingerprint the operator did not ask
// for.
func ResolveProfile(name string) (utls.ClientHelloID, bool) {
	normalized := NormalizeProfileName(name)
	if normalized == "" {
		return profiles[DefaultProfile], true
	}
	id, ok := profiles[normalized]
	return id, ok
}

// NormalizeProfileName trims and lowercases a profile name for comparison.
func NormalizeProfileName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// IsValidProfile reports whether name identifies a known profile. An empty name
// is valid and selects DefaultProfile.
func IsValidProfile(name string) bool {
	if NormalizeProfileName(name) == "" {
		return true
	}
	_, ok := ResolveProfile(name)
	return ok
}

// AvailableProfiles lists the supported profile names in a stable order, for
// configuration validation messages and management surfaces.
func AvailableProfiles() []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
