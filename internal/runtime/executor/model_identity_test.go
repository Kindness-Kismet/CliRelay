package executor

import (
	"context"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestSameModelIdentityTreatsRoutingPrefixAsSameModel(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		upstream  string
		want      bool
	}{
		{"identical", "deepseek-v4-flash:0731", "deepseek-v4-flash:0731", true},
		{"provider prefix alias", "ollama/deepseek-v4-flash:0731", "deepseek-v4-flash:0731", true},
		{"group prefix alias", "cline-pass/deepseek-v4-flash", "deepseek-v4-flash", true},
		{"nested prefix alias", "group/ollama/gpt-oss:20b", "gpt-oss:20b", true},
		{"case insensitive", "Ollama/GPT-OSS:20B", "gpt-oss:20b", true},
		{"renaming alias", "fast", "claude-sonnet-4", false},
		{"different models", "gpt-oss:20b", "gpt-oss:120b", false},
		{"substring without separator", "xdeepseek-v4-flash", "deepseek-v4-flash", false},
		{"empty upstream", "deepseek-v4-flash", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameModelIdentity(tc.requested, tc.upstream); got != tc.want {
				t.Fatalf("sameModelIdentity(%q, %q) = %v, want %v", tc.requested, tc.upstream, got, tc.want)
			}
		})
	}
}

func TestUsageReporterDropsAliasOnlyUpstreamModel(t *testing.T) {
	reporter := newUsageReporter(context.Background(), "ollama-cloud", "ollama/deepseek-v4-flash:0731", "deepseek-v4-flash:0731", &cliproxyauth.Auth{}, false)
	if reporter.upstreamModel != "" {
		t.Fatalf("upstream model = %q, want empty for a prefix-only alias", reporter.upstreamModel)
	}
}

func TestUsageReporterKeepsRenamingUpstreamModel(t *testing.T) {
	reporter := newUsageReporter(context.Background(), "claude", "fast", "claude-sonnet-4", &cliproxyauth.Auth{}, false)
	if reporter.upstreamModel != "claude-sonnet-4" {
		t.Fatalf("upstream model = %q, want claude-sonnet-4", reporter.upstreamModel)
	}
}

func TestUsageReporterSettersRejectAliasOnlyNames(t *testing.T) {
	reporter := newUsageReporter(context.Background(), "ollama-cloud", "ollama/gpt-oss:20b", "", &cliproxyauth.Auth{}, false)
	reporter.setUpstreamModel("gpt-oss:20b")
	reporter.setVisionFallbackModel("gpt-oss:20b")
	if reporter.upstreamModel != "" || reporter.visionFallbackModel != "" {
		t.Fatalf("upstream/fallback = %q/%q, want both empty", reporter.upstreamModel, reporter.visionFallbackModel)
	}

	reporter.setVisionFallbackModel("qwen3.5-plus")
	if reporter.visionFallbackModel != "qwen3.5-plus" {
		t.Fatalf("vision fallback = %q, want qwen3.5-plus", reporter.visionFallbackModel)
	}
}

// setModel re-evaluates the recorded upstream name: a vision fallback rewrites the
// logged model after construction, and the record must not keep a name that has
// meanwhile become an alias of it.
func TestUsageReporterSetModelClearsNowRedundantUpstream(t *testing.T) {
	reporter := newUsageReporter(context.Background(), "ollama-cloud", "some-other-model", "deepseek-v4-flash:0731", &cliproxyauth.Auth{}, false)
	if reporter.upstreamModel == "" {
		t.Fatal("precondition: upstream model should be recorded for unrelated models")
	}
	reporter.setModel("ollama/deepseek-v4-flash:0731")
	if reporter.upstreamModel != "" {
		t.Fatalf("upstream model = %q, want empty after the logged model became its alias", reporter.upstreamModel)
	}
}

// A retry of the same request must not be scored against its own stored prompt:
// that would report a full cache hit for work the upstream is doing again.
func TestOllamaPromptCacheEstimatorReplaysEstimateOnRetry(t *testing.T) {
	key := "ollama-retry-test"
	first := strings.Repeat("stable session prefix ", 400)
	second := first + "next turn"

	if got := newOllamaPromptCacheEstimator(key, first).estimate(1000); got != 0 {
		t.Fatalf("first turn estimate = %d, want 0", got)
	}
	turnTwo := newOllamaPromptCacheEstimator(key, second).estimate(1000)
	if turnTwo <= 0 {
		t.Fatalf("second turn estimate = %d, want a positive cache hit", turnTwo)
	}
	retry := newOllamaPromptCacheEstimator(key, second).estimate(1000)
	if retry != turnTwo {
		t.Fatalf("retry estimate = %d, want the original %d", retry, turnTwo)
	}
	if retry >= 1000 {
		t.Fatalf("retry estimate = %d, must not claim the whole prompt was cached", retry)
	}
}
