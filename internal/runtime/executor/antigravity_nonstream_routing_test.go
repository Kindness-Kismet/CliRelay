package executor

import (
	"os"
	"strings"
	"testing"
)

// Non-streaming requests must not be routed by model name.
//
// The upstream's :generateContent works for some models and not others —
// gemini-3.7-flash-* failed 41 out of 41 non-streaming calls in production
// while succeeding on every streaming one. The previous dispatch sent only
// `claude` and `gemini-3-pro` down the working path, so each newly shipped
// model silently inherited the broken one until someone noticed.
//
// This asserts on the source because the failure mode is a routing decision
// that no unit-level call can observe: both paths return a valid response
// against a stub, and only the chosen URL differs.
func TestExecuteRoutesEveryModelThroughTheStreamEndpoint(t *testing.T) {
	source, err := os.ReadFile("antigravity_executor.go")
	if err != nil {
		t.Fatalf("read executor source: %v", err)
	}
	body := string(source)
	start := strings.Index(body, "func (e *AntigravityExecutor) Execute(")
	if start < 0 {
		t.Fatal("Execute not found")
	}
	end := strings.Index(body[start:], "\nfunc ")
	if end < 0 {
		end = len(body) - start
	}
	execute := body[start : start+end]

	if !strings.Contains(execute, "executeViaStreamEndpoint") {
		t.Fatal("Execute must delegate to executeViaStreamEndpoint")
	}
	// A model-name condition here is the bug this test exists to prevent.
	for _, marker := range []string{`"gemini-3-pro"`, "isClaude", `"claude"`} {
		if strings.Contains(execute, marker) {
			t.Fatalf("Execute routes on model name (%s); every model must take the same path", marker)
		}
	}
}

// The detour is only meaningful if it actually asks for the streaming endpoint.
func TestNonStreamPathRequestsTheStreamingEndpoint(t *testing.T) {
	source, err := os.ReadFile("antigravity_nonstream.go")
	if err != nil {
		t.Fatalf("read nonstream source: %v", err)
	}
	body := string(source)
	if !strings.Contains(body, "executeViaStreamEndpoint") {
		t.Fatal("executeViaStreamEndpoint not found")
	}
	if !strings.Contains(body, "convertStreamToNonStream") {
		t.Fatal("the streamed reply must be assembled back into one response")
	}
	// buildRequest's stream flag selects :streamGenerateContent over
	// :generateContent; passing false here would silently restore the bug.
	if !strings.Contains(body, "execCtx.BaseModel, translated, true,") {
		t.Fatal("buildRequest must be called with stream=true so the streaming endpoint is used")
	}
}
