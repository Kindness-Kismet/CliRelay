package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// A request that dies on the upstream's first answer never reaches the code
// that reads the response body, which is where the streaming flag used to be
// derived. Production filed every such failure as non-streaming: the resulting
// bucket held non-streaming successes plus all failures, so it showed a ~90%
// failure rate for a model whose streaming traffic looked flawless.
func TestAntigravityStreamFailureKeepsStreamingClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`))
	}))
	defer server.Close()

	usagePlugin := &usageCapturePlugin{records: make(chan cliproxyusage.Record, 8)}
	cliproxyusage.RegisterPlugin(usagePlugin)

	_, err := NewAntigravityExecutor(&config.Config{}).ExecuteStream(context.Background(), antigravityTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash-high",
		Payload: []byte(`{"stream":true,"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("antigravity")})
	if err == nil {
		t.Fatal("ExecuteStream() error = nil, want the upstream 429")
	}

	record := waitForAntigravityFailureRecord(t, usagePlugin.records)
	if !record.Streaming {
		t.Fatal("a client-streamed request that fails upstream must stay classified as streaming")
	}
}

// The mirror case: Antigravity serves non-streaming requests from the streaming
// endpoint too, so the executor reads SSE either way. Only the client's own
// request mode may decide the classification.
func TestAntigravityNonStreamFailureStaysNonStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`))
	}))
	defer server.Close()

	usagePlugin := &usageCapturePlugin{records: make(chan cliproxyusage.Record, 8)}
	cliproxyusage.RegisterPlugin(usagePlugin)

	_, err := NewAntigravityExecutor(&config.Config{}).Execute(context.Background(), antigravityTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash-high",
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("antigravity")})
	if err == nil {
		t.Fatal("Execute() error = nil, want the upstream 429")
	}

	record := waitForAntigravityFailureRecord(t, usagePlugin.records)
	if record.Streaming {
		t.Fatal("a non-streaming client request must not be reclassified by the upstream call shape")
	}
}

func waitForAntigravityFailureRecord(t *testing.T, records <-chan cliproxyusage.Record) cliproxyusage.Record {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case record := <-records:
			if record.Provider == antigravityAuthType && record.Failed {
				return record
			}
		case <-timer.C:
			t.Fatal("timed out waiting for antigravity failure usage record")
		}
	}
}
