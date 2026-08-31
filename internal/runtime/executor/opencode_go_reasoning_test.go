package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// Unit: opencodeGoNeedsReasoningInjection
// ---------------------------------------------------------------------------

func TestOpenCodeGoNeedsReasoningInjection(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"deepseek-v4-flash", true},
		{"deepseek-v4-pro", true},
		{"deepseek-v3", true},
		{"DEEPSEEK-v4-flash", true},
		{"DeepSeek-V4-Flash", true},
		{"deepseek-v4-flash:think", true},
		{"gpt-5.5", false},
		{"gpt-4o", false},
		{"claude-sonnet-4-5", false},
		{"gemini-2.5-pro", false},
		{"minimax-m2.7", false},
		{"qwen3.5-plus", false},
	}
	for _, tc := range tests {
		got := opencodeGoNeedsReasoningInjection(tc.model)
		if got != tc.want {
			t.Errorf("opencodeGoNeedsReasoningInjection(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Unit: opencodeGoSessionID
// ---------------------------------------------------------------------------

func TestOpenCodeGoSessionID_FromHeaders(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Session-Id", "sess_abc123")
	opts := cliproxyexecutor.Options{Headers: headers}
	if got := opencodeGoSessionID(opts, nil); got != "sess_abc123" {
		t.Errorf("opencodeGoSessionID = %q, want sess_abc123", got)
	}
}

func TestOpenCodeGoSessionID_FromFallbackHeader(t *testing.T) {
	headers := make(http.Header)
	headers.Set("X-Client-Request-Id", "req_xyz789")
	opts := cliproxyexecutor.Options{Headers: headers}
	if got := opencodeGoSessionID(opts, nil); got != "req_xyz789" {
		t.Errorf("opencodeGoSessionID = %q, want req_xyz789", got)
	}
}

func TestOpenCodeGoSessionID_FromMetadata(t *testing.T) {
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "meta_session_001",
		},
	}
	if got := opencodeGoSessionID(opts, nil); got != "meta_session_001" {
		t.Errorf("opencodeGoSessionID = %q, want meta_session_001", got)
	}
}

func TestOpenCodeGoSessionID_PreferSessionIDOverClientRequest(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Session-Id", "primary_session")
	headers.Set("X-Client-Request-Id", "secondary_req")
	opts := cliproxyexecutor.Options{Headers: headers}
	if got := opencodeGoSessionID(opts, nil); got != "primary_session" {
		t.Errorf("opencodeGoSessionID = %q, want primary_session", got)
	}
}

func TestOpenCodeGoSessionID_Empty(t *testing.T) {
	opts := cliproxyexecutor.Options{}
	if got := opencodeGoSessionID(opts, nil); got != "" {
		t.Errorf("opencodeGoSessionID = %q, want empty", got)
	}
}

func TestOpenCodeGoSessionID_FromSessionStickyKey(t *testing.T) {
	// Ordinary HTTP turns never set ExecutionSessionMetadataKey (websocket only),
	// so the sticky key is what carries session identity for header-based clients.
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.SessionStickyMetadataKey: "header:x-deepseek-harness-session-id:ses_abc",
		},
	}
	if got, want := opencodeGoSessionID(opts, nil), "header:x-deepseek-harness-session-id:ses_abc"; got != want {
		t.Errorf("opencodeGoSessionID = %q, want %q", got, want)
	}
}

func TestOpenCodeGoSessionID_StickyKeyBeatsAuthFallback(t *testing.T) {
	// The regression this guards: two concurrent sessions on ONE api key must not
	// collapse onto the same reasoning slot (reasoningCacheMaxEntriesPerKey == 1),
	// or each turn is injected with the other session's reasoning_content and the
	// upstream prefix cache is lost.
	auth := &cliproxyauth.Auth{ID: "auth-shared"}
	first := opencodeGoSessionID(cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.SessionStickyMetadataKey: "header:x-deepseek-harness-session-id:ses_first",
		},
	}, auth)
	second := opencodeGoSessionID(cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.SessionStickyMetadataKey: "header:x-deepseek-harness-session-id:ses_second",
		},
	}, auth)
	if first == second {
		t.Fatalf("expected distinct session ids for distinct sticky keys, both were %q", first)
	}
	if first == auth.ID || second == auth.ID {
		t.Fatalf("expected sticky key to win over auth fallback, got %q and %q", first, second)
	}
}

func TestOpenCodeGoSessionID_ExecutionSessionBeatsStickyKey(t *testing.T) {
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "exec_session",
			cliproxyexecutor.SessionStickyMetadataKey:    "header:x-deepseek-harness-session-id:ses_abc",
		},
	}
	if got := opencodeGoSessionID(opts, nil); got != "exec_session" {
		t.Errorf("opencodeGoSessionID = %q, want exec_session", got)
	}
}

func TestOpenCodeGoSessionID_BlankMetadataFallsThrough(t *testing.T) {
	// A present-but-blank metadata value must not short-circuit the remaining
	// candidates: blanks previously returned "" and skipped the auth fallback.
	auth := &cliproxyauth.Auth{ID: "auth-1"}
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "   ",
			cliproxyexecutor.SessionStickyMetadataKey:    "  ",
		},
	}
	if got := opencodeGoSessionID(opts, auth); got != "auth-1" {
		t.Errorf("opencodeGoSessionID = %q, want auth-1", got)
	}
}

func TestOpenCodeGoSessionID_FallbackToAuthID(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "opencode-go:apikey:abc123"}
	opts := cliproxyexecutor.Options{}
	if got := opencodeGoSessionID(opts, auth); got != "opencode-go:apikey:abc123" {
		t.Errorf("opencodeGoSessionID = %q, want opencode-go:apikey:abc123", got)
	}
}

func TestOpenCodeGoSessionID_PreferSessionOverAuthID(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "auth_id_fallback"}
	headers := make(http.Header)
	headers.Set("Session-Id", "session_id_preferred")
	opts := cliproxyexecutor.Options{Headers: headers}
	if got := opencodeGoSessionID(opts, auth); got != "session_id_preferred" {
		t.Errorf("opencodeGoSessionID = %q, want session_id_preferred", got)
	}
}

// ---------------------------------------------------------------------------
// Unit: reasoning cache (open/close helpers)
// ---------------------------------------------------------------------------

func TestOpenCodeGoReasoningCache_SetGet(t *testing.T) {
	// Clean state
	reasoningCache = sync.Map{}

	opencodeGoCacheReasoningContent("deepseek-v4-flash", "sess_001", "previous reasoning step 1")
	got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_001")
	if got != "previous reasoning step 1" {
		t.Errorf("Get cached = %q, want %q", got, "previous reasoning step 1")
	}
}

func TestOpenCodeGoReasoningCache_Miss(t *testing.T) {
	reasoningCache = sync.Map{}
	if got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "no_such_session"); got != "" {
		t.Errorf("expected empty for cache miss, got %q", got)
	}
}

func TestOpenCodeGoReasoningCache_EmptyModelOrSession(t *testing.T) {
	reasoningCache = sync.Map{}

	opencodeGoCacheReasoningContent("", "sess_001", "content")
	opencodeGoCacheReasoningContent("deepseek-v4-flash", "", "content")
	opencodeGoCacheReasoningContent("deepseek-v4-flash", "sess_001", "")

	if got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_001"); got != "" {
		t.Errorf("expected empty when nothing was cached, got %q", got)
	}
}

func TestOpenCodeGoReasoningCache_TTLExpiry(t *testing.T) {
	reasoningCache = sync.Map{}

	// Use a short TTL for testing
	origTTL := reasoningContentCacheTTL
	reasoningContentCacheTTL = 1 * time.Millisecond
	t.Cleanup(func() { reasoningContentCacheTTL = origTTL })

	opencodeGoCacheReasoningContent("deepseek-v4-flash", "sess_ttl", "some reasoning")
	time.Sleep(2 * time.Millisecond)

	if got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_ttl"); got != "" {
		t.Errorf("expected empty after TTL expiry, got %q", got)
	}
}

func TestOpenCodeGoReasoningCache_SlidingExpiry(t *testing.T) {
	reasoningCache = sync.Map{}

	origTTL := reasoningContentCacheTTL
	reasoningContentCacheTTL = 50 * time.Millisecond
	t.Cleanup(func() { reasoningContentCacheTTL = origTTL })

	opencodeGoCacheReasoningContent("deepseek-v4-flash", "sess_slide", "some reasoning")
	// Access within TTL - should refresh
	time.Sleep(20 * time.Millisecond)
	if got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_slide"); got != "some reasoning" {
		t.Errorf("expected content within TTL, got %q", got)
	}
	// Should still be valid after another 20ms (sliding window refreshed)
	time.Sleep(20 * time.Millisecond)
	if got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_slide"); got != "some reasoning" {
		t.Errorf("expected content still valid with sliding expiry, got %q", got)
	}
}

func TestOpenCodeGoReasoningCache_AccumulateOnSecondSet(t *testing.T) {
	reasoningCache = sync.Map{}

	opencodeGoCacheReasoningContent("model-a", "sess_x", "first")
	opencodeGoCacheReasoningContent("model-a", "sess_x", "second")
	got := opencodeGoGetCachedReasoningContent("model-a", "sess_x")
	if got != "second" {
		t.Errorf("expected second set overwrites first, got %q", got)
	}
}

func TestOpenCodeGoReasoningCache_DifferentModelSameSession(t *testing.T) {
	reasoningCache = sync.Map{}

	opencodeGoCacheReasoningContent("model-a", "sess_same", "content a")
	opencodeGoCacheReasoningContent("model-b", "sess_same", "content b")

	gotA := opencodeGoGetCachedReasoningContent("model-a", "sess_same")
	gotB := opencodeGoGetCachedReasoningContent("model-b", "sess_same")
	if gotA != "content a" || gotB != "content b" {
		t.Errorf("model-a=%q, model-b=%q", gotA, gotB)
	}
}

// ---------------------------------------------------------------------------
// Unit: opencodeGoCacheReasoningFromNonStream
// ---------------------------------------------------------------------------

func TestOpenCodeGoCacheReasoningFromNonStream_ChatCompletions(t *testing.T) {
	reasoningCache = sync.Map{}

	payload := []byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hello","reasoning_content":"thinking steps here"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	opencodeGoCacheReasoningFromNonStream(payload, "deepseek-v4-flash", "sess_001")

	got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_001")
	if got != "thinking steps here" {
		t.Errorf("got %q, want %q", got, "thinking steps here")
	}
}

func TestOpenCodeGoCacheReasoningFromNonStream_NoReasoning(t *testing.T) {
	reasoningCache = sync.Map{}

	payload := []byte(`{"id":"chatcmpl_2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"No thinking"}}]}`)
	opencodeGoCacheReasoningFromNonStream(payload, "deepseek-v4-flash", "sess_002")

	if got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_002"); got != "" {
		t.Errorf("expected empty when no reasoning_content, got %q", got)
	}
}

func TestOpenCodeGoCacheReasoningFromNonStream_EmptyPayload(t *testing.T) {
	reasoningCache = sync.Map{}

	opencodeGoCacheReasoningFromNonStream(nil, "deepseek-v4-flash", "sess_003")
	opencodeGoCacheReasoningFromNonStream([]byte{}, "deepseek-v4-flash", "sess_003")
	opencodeGoCacheReasoningFromNonStream([]byte(`invalid json`), "deepseek-v4-flash", "sess_003")

	if got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_003"); got != "" {
		t.Errorf("expected empty for invalid payloads, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Unit: opencodeGoCacheReasoningFromStreamChunk
// ---------------------------------------------------------------------------

func TestOpenCodeGoCacheReasoningFromStreamChunk_Delta(t *testing.T) {
	reasoningCache = sync.Map{}

	chunk := []byte(`data: {"id":"chunk1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"thinking"}}]}`)
	opencodeGoCacheReasoningFromStreamChunk(chunk, "deepseek-v4-flash", "sess_stream")

	got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_stream")
	if got != "thinking" {
		t.Errorf("got %q, want %q", got, "thinking")
	}
}

func TestOpenCodeGoCacheReasoningFromStreamChunk_Accumulate(t *testing.T) {
	reasoningCache = sync.Map{}

	chunks := []string{
		`data: {"id":"chunk1","choices":[{"index":0,"delta":{"reasoning_content":"step1 "}}]}`,
		`data: {"id":"chunk2","choices":[{"index":0,"delta":{"reasoning_content":"step2 "}}]}`,
		`data: {"id":"chunk3","choices":[{"index":0,"delta":{"reasoning_content":"step3"}}]}`,
	}
	for _, c := range chunks {
		opencodeGoCacheReasoningFromStreamChunk([]byte(c), "deepseek-v4-flash", "sess_acc")
	}

	got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_acc")
	if got != "step1 step2 step3" {
		t.Errorf("accumulated = %q, want %q", got, "step1 step2 step3")
	}
}

func TestOpenCodeGoCacheReasoningFromStreamChunk_NoDataPrefix(t *testing.T) {
	reasoningCache = sync.Map{}

	chunk := []byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"raw json"}}]}`)
	opencodeGoCacheReasoningFromStreamChunk(chunk, "deepseek-v4-flash", "sess_raw")

	got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_raw")
	if got != "raw json" {
		t.Errorf("got %q, want %q", got, "raw json")
	}
}

func TestOpenCodeGoCacheReasoningFromStreamChunk_InvalidJSON(t *testing.T) {
	reasoningCache = sync.Map{}

	opencodeGoCacheReasoningFromStreamChunk([]byte(`not json`), "deepseek-v4-flash", "sess_inv")
	opencodeGoCacheReasoningFromStreamChunk([]byte(`data: not json too`), "deepseek-v4-flash", "sess_inv")

	if got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_inv"); got != "" {
		t.Errorf("expected empty for invalid json, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Unit: opencodeGoInjectReasoningContentIntoPayload
// ---------------------------------------------------------------------------

func TestOpenCodeGoInjectReasoningContentInjectsIntoLastAssistant(t *testing.T) {
	reasoningCache = sync.Map{}
	reasoningContentCacheTTL = 30 * time.Minute

	opencodeGoCacheReasoningContent("deepseek-v4-flash", "sess_inj", "cached reasoning")

	payload := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"previous response"},{"role":"user","content":"next question"}]}`)
	got := opencodeGoInjectReasoningContentIntoPayload(payload, "deepseek-v4-flash", "sess_inj")

	// Verify reasoning_content was injected into the last assistant message
	msgs := gjson.GetBytes(got, "messages").Array()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	lastAssistant := msgs[1] // index 1 is the assistant message
	if rc := lastAssistant.Get("reasoning_content").String(); rc != "cached reasoning" {
		t.Errorf("last assistant reasoning_content = %q, want %q", rc, "cached reasoning")
	}
}

func TestOpenCodeGoInjectReasoningContentSkipsAssistantWithExistingReasoning(t *testing.T) {
	reasoningCache = sync.Map{}
	reasoningContentCacheTTL = 30 * time.Minute

	opencodeGoCacheReasoningContent("deepseek-v4-flash", "sess_skip", "cached reasoning")

	payload := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"already has rc","reasoning_content":"existing rc"},{"role":"user","content":"next"}]}`)
	got := opencodeGoInjectReasoningContentIntoPayload(payload, "deepseek-v4-flash", "sess_skip")

	msgs := gjson.GetBytes(got, "messages").Array()
	if rc := msgs[1].Get("reasoning_content").String(); rc != "existing rc" {
		t.Errorf("should preserve existing reasoning_content, got %q", rc)
	}
}

func TestOpenCodeGoInjectReasoningContentNoMessages(t *testing.T) {
	reasoningCache = sync.Map{}
	reasoningContentCacheTTL = 30 * time.Minute

	opencodeGoCacheReasoningContent("deepseek-v4-flash", "sess_no_msg", "cached")

	payload := []byte(`{"model":"deepseek-v4-flash","prompt":"just a prompt"}`)
	got := opencodeGoInjectReasoningContentIntoPayload(payload, "deepseek-v4-flash", "sess_no_msg")
	if string(got) != string(payload) {
		t.Errorf("payload should not change when no messages array")
	}
}

func TestOpenCodeGoInjectReasoningContentNoCache(t *testing.T) {
	reasoningCache = sync.Map{}

	payload := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"resp"}]}`)
	got := opencodeGoInjectReasoningContentIntoPayload(payload, "deepseek-v4-flash", "sess_no_cache")
	// Even when cache is empty, we inject empty reasoning_content
	// to satisfy DeepSeek's thinking mode validation.
	if !gjson.GetBytes(got, "messages.1.reasoning_content").Exists() {
		t.Error("should inject empty reasoning_content even when cache is empty")
	}
	if rc := gjson.GetBytes(got, "messages.1.reasoning_content").String(); rc != "" {
		t.Errorf("reasoning_content should be empty string when cache is empty, got %q", rc)
	}
}

func TestOpenCodeGoInjectReasoningContentMultipleAssistants(t *testing.T) {
	reasoningCache = sync.Map{}
	reasoningContentCacheTTL = 30 * time.Minute

	opencodeGoCacheReasoningContent("deepseek-v4-flash", "sess_multi", "cached reasoning")

	payload := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"q1"},{"role":"assistant","content":"a1"},{"role":"user","content":"q2"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"test","arguments":"{}"}}]},{"role":"tool","content":"result","tool_call_id":"call_1"},{"role":"assistant","content":"a3"}]}`)
	got := opencodeGoInjectReasoningContentIntoPayload(payload, "deepseek-v4-flash", "sess_multi")

	msgs := gjson.GetBytes(got, "messages").Array()
	// All assistant messages missing reasoning_content should get it injected.
	// a1 (index 1) - text assistant, should get the cached reasoning_content
	if rc := msgs[1].Get("reasoning_content").String(); rc != "cached reasoning" {
		t.Errorf("assistant at index 1 should get cached reasoning_content, got %q", rc)
	}
	// a2 (index 3) - tool_call assistant, should get EMPTY reasoning_content
	if rc := msgs[3].Get("reasoning_content").String(); rc != "" {
		t.Errorf("assistant with tool_calls should get empty reasoning_content, got %q", rc)
	}
	// a3 (index 5) - text assistant, should get the cached reasoning_content
	if rc := msgs[5].Get("reasoning_content").String(); rc != "cached reasoning" {
		t.Errorf("last assistant at index 5 should get cached reasoning_content, got %q", rc)
	}
}

func TestOpenCodeGoInjectReasoningContentPreservesModelName(t *testing.T) {
	reasoningCache = sync.Map{}
	reasoningContentCacheTTL = 30 * time.Minute

	opencodeGoCacheReasoningContent("deepseek-v4-flash", "sess_pres", "cached")

	payload := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"resp"}]}`)
	got := opencodeGoInjectReasoningContentIntoPayload(payload, "deepseek-v4-flash", "sess_pres")

	if model := gjson.GetBytes(got, "model").String(); model != "deepseek-v4-flash" {
		t.Errorf("model changed to %q", model)
	}
}

// ---------------------------------------------------------------------------
// Integration: Execute with reasoning_content caching (non-streaming)
// ---------------------------------------------------------------------------

func TestOpenCodeGoExecutorReasoningCacheNonStream(t *testing.T) {
	reasoningCache = sync.Map{}
	reasoningContentCacheTTL = 30 * time.Minute

	// Simulate a first request that gets a response with reasoning_content
	var gotBodies [][]byte
	reqCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, body)
		reqCount++
		w.Header().Set("Content-Type", "application/json")
		// First response includes reasoning_content
		if reqCount == 1 {
			_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"first response","reasoning_content":"first reasoning step"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		} else {
			// Second response (should have reasoning_content injected)
			_, _ = w.Write([]byte(`{"id":"chatcmpl_2","object":"chat.completion","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"second response"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		}
	}))
	defer server.Close()

	oldBaseURL := opencodeGoBaseURL
	opencodeGoBaseURL = server.URL + "/v1"
	t.Cleanup(func() { opencodeGoBaseURL = oldBaseURL })

	exec := NewOpenCodeGoExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key"}}

	// First request: inject reasoning_content (cache empty, so no injection)
	sessionHeaders := make(http.Header)
	sessionHeaders.Set("Session-Id", "integ_test_session")
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers:      sessionHeaders,
	}

	payload1 := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"first question"}]}`)
	resp1, err1 := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: payload1,
	}, opts)
	if err1 != nil {
		t.Fatalf("first Execute error: %v", err1)
	}
	if txt := gjson.GetBytes(resp1.Payload, "choices.0.message.content").String(); txt != "first response" {
		t.Errorf("first response text = %q, want %q", txt, "first response")
	}

	// Verify reasoning_content was cached
	cached := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "integ_test_session")
	if cached != "first reasoning step" {
		t.Errorf("cached reasoning = %q, want %q", cached, "first reasoning step")
	}

	// Second request: should have reasoning_content injected into assistant messages
	payload2 := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"first question"},{"role":"assistant","content":"first response"},{"role":"user","content":"second question"}]}`)
	resp2, err2 := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: payload2,
	}, opts)
	if err2 != nil {
		t.Fatalf("second Execute error: %v", err2)
	}
	if txt := gjson.GetBytes(resp2.Payload, "choices.0.message.content").String(); txt != "second response" {
		t.Errorf("second response text = %q, want %q", txt, "second response")
	}

	// Verify the second request sent to upstream had reasoning_content injected (check request 2: index 1 in gotBodies)
	if len(gotBodies) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(gotBodies))
	}
	req2Body := gotBodies[1]
	msgs := gjson.GetBytes(req2Body, "messages").Array()
	if len(msgs) < 2 {
		t.Fatalf("req2 messages length = %d", len(msgs))
	}
	// The last assistant message (index 1) should have reasoning_content
	assistantMsg := msgs[1]
	if !assistantMsg.Get("reasoning_content").Exists() {
		t.Fatalf("second request assistant message missing reasoning_content; body=%s", string(req2Body))
	}
	if rc := assistantMsg.Get("reasoning_content").String(); rc != "first reasoning step" {
		t.Errorf("injected reasoning_content = %q, want %q; body=%s", rc, "first reasoning step", string(req2Body))
	}
}

// ---------------------------------------------------------------------------
// Integration: ExecuteStream with reasoning_content caching
// ---------------------------------------------------------------------------

func TestOpenCodeGoExecutorReasoningCacheStream(t *testing.T) {
	reasoningCache = sync.Map{}
	reasoningContentCacheTTL = 30 * time.Minute

	var gotBodies [][]byte
	reqCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, body)
		reqCount++
		w.Header().Set("Content-Type", "text/event-stream")
		if reqCount == 1 {
			// First streaming response with reasoning_content
			_, _ = w.Write([]byte(`data: {"id":"chunk1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"step by step "}}]}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"id":"chunk2","choices":[{"index":0,"delta":{"reasoning_content":"thinking "}}]}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"id":"chunk3","choices":[{"index":0,"delta":{"content":"final answer"}}]}` + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		} else {
			_, _ = w.Write([]byte(`data: {"id":"chunk1","choices":[{"index":0,"delta":{"content":"second answer"}}]}` + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
	}))
	defer server.Close()

	oldBaseURL := opencodeGoBaseURL
	opencodeGoBaseURL = server.URL + "/v1"
	t.Cleanup(func() { opencodeGoBaseURL = oldBaseURL })

	exec := NewOpenCodeGoExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key"}}

	sessionHeaders := make(http.Header)
	sessionHeaders.Set("Session-Id", "integ_stream_session")
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers:      sessionHeaders,
		Stream:       true,
	}

	// First streaming request
	payload1 := []byte(`{"model":"deepseek-v4-flash","stream":true,"messages":[{"role":"user","content":"first q"}]}`)
	result1, err1 := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: payload1,
	}, opts)
	if err1 != nil {
		t.Fatalf("first ExecuteStream error: %v", err1)
	}

	var chunks1 strings.Builder
	for chunk := range result1.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		chunks1.Write(chunk.Payload)
	}
	if !strings.Contains(chunks1.String(), "final answer") {
		t.Errorf("first stream should contain final answer; got %s", chunks1.String())
	}

	// Check that reasoning_content was accumulated in cache
	cached := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "integ_stream_session")
	if cached != "step by step thinking " {
		t.Errorf("cached reasoning = %q, want %q", cached, "step by step thinking ")
	}

	// Second streaming request: should have reasoning_content injected
	payload2 := []byte(`{"model":"deepseek-v4-flash","stream":true,"messages":[{"role":"user","content":"first q"},{"role":"assistant","content":"final answer"},{"role":"user","content":"second q"}]}`)
	result2, err2 := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: payload2,
	}, opts)
	if err2 != nil {
		t.Fatalf("second ExecuteStream error: %v", err2)
	}

	var chunks2 strings.Builder
	for chunk := range result2.Chunks {
		if chunk.Err != nil {
			t.Fatalf("second stream chunk error: %v", chunk.Err)
		}
		chunks2.Write(chunk.Payload)
	}
	if !strings.Contains(chunks2.String(), "second answer") {
		t.Errorf("second stream should contain second answer; got %s", chunks2.String())
	}

	// Verify the second request had reasoning_content injected
	if len(gotBodies) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(gotBodies))
	}
	req2Body := gotBodies[1]
	msgs := gjson.GetBytes(req2Body, "messages").Array()
	if len(msgs) < 2 {
		t.Fatalf("req2 messages length = %d", len(msgs))
	}
	assistantMsg := msgs[1]
	if !assistantMsg.Get("reasoning_content").Exists() {
		t.Fatalf("second streaming request missing reasoning_content; body=%s", string(req2Body))
	}
	if rc := assistantMsg.Get("reasoning_content").String(); rc != "step by step thinking " {
		t.Errorf("injected reasoning_content = %q, want %q; body=%s", rc, "step by step thinking ", string(req2Body))
	}
}

// ---------------------------------------------------------------------------
// Integration: Non-deepseek model should NOT get reasoning injection
// ---------------------------------------------------------------------------

func TestOpenCodeGoExecutorSkipsReasoningForNonDeepSeek(t *testing.T) {
	reasoningCache = sync.Map{}

	var gotBodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","created":1,"model":"gpt-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer server.Close()

	oldBaseURL := opencodeGoBaseURL
	opencodeGoBaseURL = server.URL + "/v1"
	t.Cleanup(func() { opencodeGoBaseURL = oldBaseURL })

	exec := NewOpenCodeGoExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key"}}

	sessionHeaders := make(http.Header)
	sessionHeaders.Set("Session-Id", "gpt_session")
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers:      sessionHeaders,
	}

	payload1 := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: payload1,
	}, opts)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// GPT model should NOT have reasoning_content cached
	if got := opencodeGoGetCachedReasoningContent("gpt-5.5", "gpt_session"); got != "" {
		t.Errorf("should not cache reasoning for non-deepseek model, got %q", got)
	}

	// Second request should not be modified
	payload2 := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":"follow up"}]}`)
	_, err = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: payload2,
	}, opts)
	if err != nil {
		t.Fatalf("second Execute error: %v", err)
	}

	if len(gotBodies) < 2 {
		t.Fatalf("expected 2 requests, got %d", len(gotBodies))
	}
	req2Body := gotBodies[1]
	if gjson.GetBytes(req2Body, "messages.1.reasoning_content").Exists() {
		t.Errorf("non-deepseek request should not have reasoning_content injected; body=%s", string(req2Body))
	}
}

// ---------------------------------------------------------------------------
// Integration: messages path (e.g., minimax) should NOT get reasoning injection
// ---------------------------------------------------------------------------

func TestOpenCodeGoExecutorSkipsReasoningForMessagesModels(t *testing.T) {
	reasoningCache = sync.Map{}
	reasoningContentCacheTTL = 30 * time.Minute

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"minimax-m2.7","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`))
	}))
	defer server.Close()

	oldBaseURL := opencodeGoBaseURL
	opencodeGoBaseURL = server.URL + "/v1"
	t.Cleanup(func() { opencodeGoBaseURL = oldBaseURL })

	exec := NewOpenCodeGoExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key"}}

	sessionHeaders := make(http.Header)
	sessionHeaders.Set("Session-Id", "minimax_session")
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers:      sessionHeaders,
	}

	payload := []byte(`{"model":"minimax-m2.7","messages":[{"role":"user","content":"hi"}]}`)
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "minimax-m2.7",
		Payload: payload,
	}, opts)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// minimax should NOT have reasoning_content cached
	if got := opencodeGoGetCachedReasoningContent("minimax-m2.7", "minimax_session"); got != "" {
		t.Errorf("should not cache reasoning for minimax, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Unit: inject into Responses API (input array) format
// ---------------------------------------------------------------------------

func TestOpenCodeGoInjectReasoningContentResponsesAPI(t *testing.T) {
	reasoningCache = sync.Map{}
	reasoningContentCacheTTL = 30 * time.Minute

	opencodeGoCacheReasoningContent("deepseek-v4-flash", "sess_resp", "cached reasoning")

	payload := []byte(`{"model":"deepseek-v4-flash","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]},{"role":"assistant","content":[{"type":"output_text","text":"prev response"}]},{"role":"user","content":[{"type":"input_text","text":"follow up"}]}]}`)
	got := opencodeGoInjectReasoningContentIntoPayload(payload, "deepseek-v4-flash", "sess_resp")

	input := gjson.GetBytes(got, "input").Array()
	if len(input) < 2 {
		t.Fatalf("expected at least 2 items, got %d", len(input))
	}
	assistantMsg := input[1]
	if !assistantMsg.Get("reasoning_content").Exists() {
		t.Fatalf("assistant in input should have reasoning_content; body=%s", string(got))
	}
	if rc := assistantMsg.Get("reasoning_content").String(); rc != "cached reasoning" {
		t.Errorf("injected reasoning_content = %q, want %q", rc, "cached reasoning")
	}
}

// ---------------------------------------------------------------------------
// Unit: opencodeGoWrapStreamCacheReasoning
// ---------------------------------------------------------------------------

func TestOpenCodeGoWrapStreamCacheReasoningCapturesContent(t *testing.T) {
	reasoningCache = sync.Map{}
	reasoningContentCacheTTL = 30 * time.Minute

	chunks := make(chan cliproxyexecutor.StreamChunk, 3)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"choices":[{"index":0,"delta":{"reasoning_content":"thinking steps"}}]}`)}
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"choices":[{"index":0,"delta":{"content":"answer"}}]}`)}
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]")}
	close(chunks)

	result := &cliproxyexecutor.StreamResult{
		Chunks: chunks,
	}
	wrapped := opencodeGoWrapStreamCacheReasoning(result, "deepseek-v4-flash", "sess_wrap")

	// Drain wrapped channel
	for range wrapped.Chunks {
	}

	got := opencodeGoGetCachedReasoningContent("deepseek-v4-flash", "sess_wrap")
	if got != "thinking steps" {
		t.Errorf("cached reasoning = %q, want %q", got, "thinking steps")
	}
}

func TestOpenCodeGoWrapStreamCacheReasoningNilResult(t *testing.T) {
	got := opencodeGoWrapStreamCacheReasoning(nil, "deepseek-v4-flash", "sess_nil")
	if got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

func TestOpenCodeGoWrapStreamCacheReasoningEmptySession(t *testing.T) {
	chunks := make(chan cliproxyexecutor.StreamChunk)
	close(chunks)
	result := &cliproxyexecutor.StreamResult{Chunks: chunks}
	got := opencodeGoWrapStreamCacheReasoning(result, "deepseek-v4-flash", "")
	if got == nil {
		t.Errorf("expected non-nil result even with empty session")
	}
}

func TestOpenCodeGoWrapStreamCacheReasoningPassesChunksThrough(t *testing.T) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("chunk1")}
	chunks <- cliproxyexecutor.StreamChunk{Err: io.ErrUnexpectedEOF}
	close(chunks)

	result := &cliproxyexecutor.StreamResult{Chunks: chunks}
	wrapped := opencodeGoWrapStreamCacheReasoning(result, "deepseek-v4-flash", "sess_thru")

	var seen [][]byte
	for chunk := range wrapped.Chunks {
		seen = append(seen, chunk.Payload)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(seen))
	}
}

// ---------------------------------------------------------------------------
// Unit: cache cleanup
// ---------------------------------------------------------------------------

func TestPurgeExpiredReasoningEntries(t *testing.T) {
	reasoningCache = sync.Map{}

	origTTL := reasoningContentCacheTTL
	reasoningContentCacheTTL = 10 * time.Millisecond
	t.Cleanup(func() { reasoningContentCacheTTL = origTTL })

	opencodeGoCacheReasoningContent("deepseek-v4-flash", "sess_clean", "will expire")
	time.Sleep(20 * time.Millisecond)

	purgeExpiredReasoningEntries()

	// Verify cache is empty
	val, ok := reasoningCache.Load("deepseek-v4-flash:sess_clean")
	if ok {
		t.Errorf("expected entry to be purged, got %v", val)
	}
}
