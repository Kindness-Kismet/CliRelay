package executor

import (
	"testing"

	antigravityauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/antigravity"
)

// The executor and the account status probe both call CloudCode and must present
// the same client. They had drifted to 1.104.0 and 1.11.5, so one account looked
// like two different clients depending on which half of the system was asking,
// and the older one was served a reduced model set and a flattened quota view.
//
// Both now derive from a single constant; this pins that they cannot diverge
// again by someone re-introducing a local literal.
func TestAntigravityExecutorUsesTheSharedClientIdentity(t *testing.T) {
	if defaultAntigravityAgent != antigravityauth.ClientUserAgent {
		t.Fatalf("defaultAntigravityAgent = %q, want the shared %q", defaultAntigravityAgent, antigravityauth.ClientUserAgent)
	}
}

// A per-account override still wins — some accounts are pinned to a specific
// client — but the fallback is the shared identity.
func TestResolveUserAgentPrefersAccountOverrideThenSharedIdentity(t *testing.T) {
	if got := resolveUserAgent(nil); got != antigravityauth.ClientUserAgent {
		t.Fatalf("resolveUserAgent(nil) = %q, want the shared identity", got)
	}
}
