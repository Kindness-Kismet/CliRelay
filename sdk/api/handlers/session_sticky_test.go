package handlers

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newStickyContext(t *testing.T, headers map[string]string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	for name, value := range headers {
		c.Request.Header.Set(name, value)
	}
	return c
}

func TestRequestSessionStickyHeaderKey_DeepSeekHarness(t *testing.T) {
	// DeepSeek Harness sends the header lowercased on every turn; Go canonicalizes
	// it, so the wire casing must not change the derived sticky key.
	for _, header := range []string{
		"x-deepseek-harness-session-id",
		"X-Deepseek-Harness-Session-Id",
		"X-DEEPSEEK-HARNESS-SESSION-ID",
	} {
		c := newStickyContext(t, map[string]string{header: "ses_abc123"})
		got := requestSessionStickyHeaderKey(c)
		want := "header:x-deepseek-harness-session-id:ses_abc123"
		if got != want {
			t.Fatalf("header %q: got %q, want %q", header, got, want)
		}
	}
}

func TestRequestSessionStickyHeaderKey_DeepSeekHarnessDistinctSessions(t *testing.T) {
	// Two concurrent harness sessions must not collapse onto one auth binding:
	// each keeps its own upstream account, and therefore its own prefix cache.
	first := requestSessionStickyHeaderKey(newStickyContext(t, map[string]string{
		"x-deepseek-harness-session-id": "ses_first",
	}))
	second := requestSessionStickyHeaderKey(newStickyContext(t, map[string]string{
		"x-deepseek-harness-session-id": "ses_second",
	}))
	if first == "" || second == "" {
		t.Fatalf("expected non-empty sticky keys, got %q and %q", first, second)
	}
	if first == second {
		t.Fatalf("expected distinct sticky keys, both were %q", first)
	}
}

func TestRequestSessionStickyHeaderKey_GenericHeaderStillWins(t *testing.T) {
	// The generic header keeps its existing precedence; adding the harness entry
	// must not reorder callers that already send Session-Id.
	c := newStickyContext(t, map[string]string{
		"Session-Id":                    "generic",
		"x-deepseek-harness-session-id": "ses_abc123",
	})
	if got, want := requestSessionStickyHeaderKey(c), "header:session-id:generic"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRequestSessionStickyHeaderKey_NoHeaders(t *testing.T) {
	// No recognized header means no seed at all: applySessionPromptCacheKey then
	// emits no prompt_cache_key rather than inventing an unstable one.
	if got := requestSessionStickyHeaderKey(newStickyContext(t, nil)); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := requestSessionStickyHeaderKey(nil); got != "" {
		t.Fatalf("nil context: got %q, want empty", got)
	}
}

func TestRequestSessionStickyHeaderKey_BlankValueIgnored(t *testing.T) {
	// A present-but-blank header must fall through to the next candidate instead
	// of binding every such request to one shared key.
	c := newStickyContext(t, map[string]string{
		"x-deepseek-harness-session-id": "   ",
		"X-Conversation-Id":             "conv-1",
	})
	if got, want := requestSessionStickyHeaderKey(c), "header:x-conversation-id:conv-1"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
