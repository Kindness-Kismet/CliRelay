package util

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Gemini accepts only "user" and "model" in contents[].role and rejects
// anything else with a bare 400 INVALID_ARGUMENT that names no field.
func TestNormalizeGeminiContentRole(t *testing.T) {
	cases := map[string]string{
		"user":      "user",
		"assistant": "model",
		"model":     "model",
		// Claude Code sends system entries inside `messages` alongside the
		// top-level `system` field. Forwarded verbatim, they failed the whole
		// request; as "user" the text still reaches the model.
		"system": "user",
		// Roles this code has never seen must not reach the upstream either.
		"tool":      "user",
		"developer": "user",
		"":          "user",
	}
	for input, want := range cases {
		if got := NormalizeGeminiContentRole(input); got != want {
			t.Fatalf("role %q -> %q, want %q", input, got, want)
		}
	}
}

// The payload-level pass is what actually protects the upstream: it runs at the
// executor, after any translator has produced the request, so a role no
// translator normalised still cannot escape.
func TestNormalizeGeminiContentRolesRewritesPayload(t *testing.T) {
	payload := `{"request":{"contents":[
		{"role":"user","parts":[{"text":"one"}]},
		{"role":"system","parts":[{"text":"stray"}]},
		{"role":"assistant","parts":[{"text":"two"}]},
		{"role":"tool","parts":[{"text":"three"}]}
	]}}`
	out := NormalizeGeminiContentRoles(payload, "request.contents")

	roles := gjson.Get(out, "request.contents.#.role").Array()
	want := []string{"user", "user", "model", "user"}
	if len(roles) != len(want) {
		t.Fatalf("contents = %d, want %d", len(roles), len(want))
	}
	for i, role := range roles {
		if role.String() != want[i] {
			t.Fatalf("contents[%d].role = %q, want %q", i, role.String(), want[i])
		}
	}
	// Only the role is rewritten; the turn's text must survive.
	if !strings.Contains(out, "stray") {
		t.Fatal("text of the rewritten turn was dropped")
	}
}

// A payload without contents must pass through untouched rather than being
// rewritten into something the upstream cannot parse.
func TestNormalizeGeminiContentRolesLeavesOtherPayloadsAlone(t *testing.T) {
	for _, payload := range []string{`{}`, `{"request":{}}`, `{"request":{"contents":"nope"}}`} {
		if got := NormalizeGeminiContentRoles(payload, "request.contents"); got != payload {
			t.Fatalf("payload %s was rewritten to %s", payload, got)
		}
	}
}
