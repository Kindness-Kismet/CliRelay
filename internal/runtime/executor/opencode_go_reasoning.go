package executor

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	reasoningContentCacheTTL       = 30 * time.Minute
	reasoningCacheCleanupInterval  = 5 * time.Minute
	reasoningCacheMaxEntriesPerKey = 1
)

var (
	reasoningCache     sync.Map
	reasoningCacheOnce sync.Once
)

type reasoningEntry struct {
	content   string
	timestamp time.Time
}

func startReasoningCacheCleanup() {
	reasoningCacheOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(reasoningCacheCleanupInterval)
			defer ticker.Stop()
			for range ticker.C {
				purgeExpiredReasoningEntries()
			}
		}()
	})
}

func purgeExpiredReasoningEntries() {
	now := time.Now()
	reasoningCache.Range(func(key, value any) bool {
		entry := value.(reasoningEntry)
		if now.Sub(entry.timestamp) > reasoningContentCacheTTL {
			reasoningCache.Delete(key)
		}
		return true
	})
}

// opencodeGoSessionID extracts the session identifier from executor options.
// Falls back to the auth ID (which is unique per API key) when session headers
// are not forwarded to the executor.
// Returns empty string if no identifier is found at all.
func opencodeGoSessionID(opts cliproxyexecutor.Options, auth *cliproxyauth.Auth) string {
	if opts.Headers != nil {
		if sessionID := opts.Headers.Get("Session-Id"); sessionID != "" {
			return sessionID
		}
		if sessionID := opts.Headers.Get("X-Client-Request-Id"); sessionID != "" {
			return sessionID
		}
	}
	if raw, ok := opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey]; ok {
		if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	// ExecutionSessionMetadataKey is only set on the websocket path, so ordinary
	// HTTP turns from clients that send a client-specific session header (DeepSeek
	// Harness: X-Deepseek-Harness-Session-Id, Grok: X-Grok-Session-Id) would reach
	// the auth-ID fallback below. That collapses every concurrent session of one
	// API key onto a single cache slot (reasoningCacheMaxEntriesPerKey == 1), so
	// turns overwrite each other and get injected with another session's
	// reasoning_content — which changes the request prefix and destroys the
	// upstream prefix cache. The sticky key already carries exactly this identity.
	if raw, ok := opts.Metadata[cliproxyexecutor.SessionStickyMetadataKey]; ok {
		if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	// Fallback: use auth ID (unique per API key / user).
	// The Session-Id header is not forwarded to executors in all code paths,
	// but auth ID is always available.
	if auth != nil && strings.TrimSpace(auth.ID) != "" {
		return strings.TrimSpace(auth.ID)
	}
	return ""
}

// opencodeGoNeedsReasoningInjection checks if the model requires reasoning_content
// tracking for multi-turn conversations (e.g., DeepSeek thinking mode).
func opencodeGoNeedsReasoningInjection(model string) bool {
	base := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	return strings.HasPrefix(base, "deepseek")
}

// opencodeGoCacheReasoningContent stores reasoning_content for a given model and session.
func opencodeGoCacheReasoningContent(model, sessionID, content string) {
	if model == "" || sessionID == "" || content == "" {
		return
	}
	startReasoningCacheCleanup()
	key := model + ":" + sessionID
	reasoningCache.Store(key, reasoningEntry{
		content:   content,
		timestamp: time.Now(),
	})
}

// opencodeGoGetCachedReasoningContent retrieves reasoning_content for a given model and session.
// Returns empty string if not found or expired.
func opencodeGoGetCachedReasoningContent(model, sessionID string) string {
	if model == "" || sessionID == "" {
		return ""
	}
	val, ok := reasoningCache.Load(model + ":" + sessionID)
	if !ok {
		return ""
	}
	entry := val.(reasoningEntry)
	if time.Since(entry.timestamp) > reasoningContentCacheTTL {
		reasoningCache.Delete(model + ":" + sessionID)
		return ""
	}
	// Refresh TTL on access (sliding expiration)
	entry.timestamp = time.Now()
	reasoningCache.Store(model+":"+sessionID, entry)
	return entry.content
}

// opencodeGoCacheReasoningFromNonStream extracts reasoning_content from a non-streaming
// response payload and caches it for future requests.
func opencodeGoCacheReasoningFromNonStream(payload []byte, model, sessionID string) {
	if len(payload) == 0 || model == "" || sessionID == "" || !gjson.ValidBytes(payload) {
		return
	}
	// OpenAI chat completions format
	if content := gjson.GetBytes(payload, "choices.0.message.reasoning_content").String(); content != "" {
		opencodeGoCacheReasoningContent(model, sessionID, content)
	}
	// Anthropic messages format (via executeMessages / executeMessagesStream)
	// The response has been translated back, so reasoning_content might be in the
	// translated payload too.
}

// opencodeGoCacheReasoningFromStreamChunk extracts reasoning_content from a streaming
// SSE chunk and caches it for future requests. It should be called for every streaming
// chunk that contains a reasoning_content delta.
func opencodeGoCacheReasoningFromStreamChunk(payload []byte, model, sessionID string) {
	if len(payload) == 0 || model == "" || sessionID == "" {
		return
	}
	// Handle SSE format: "data: {...}\n\n"
	raw := payload
	dataPrefix := []byte("data: ")
	if bytes.HasPrefix(bytes.TrimSpace(payload), dataPrefix) {
		raw = bytes.TrimSpace(payload[len(dataPrefix):])
	}
	if !gjson.ValidBytes(raw) {
		return
	}
	// OpenAI chat completions streaming format
	content := gjson.GetBytes(raw, "choices.0.delta.reasoning_content").String()
	if content == "" {
		return
	}
	key := model + ":" + sessionID
	// Accumulate: append to existing entry if it exists
	val, ok := reasoningCache.Load(key)
	if ok {
		entry := val.(reasoningEntry)
		if time.Since(entry.timestamp) <= reasoningContentCacheTTL {
			content = entry.content + content
		}
	}
	opencodeGoCacheReasoningContent(model, sessionID, content)
}

// opencodeGoInjectReasoningContentIntoPayload scans the request payload for assistant
// messages that are missing reasoning_content and injects the cached content when available.
// This is needed for DeepSeek models that require reasoning_content in multi-turn thinking mode.
func opencodeGoInjectReasoningContentIntoPayload(payload []byte, model, sessionID string) []byte {
	if len(payload) == 0 || model == "" || sessionID == "" || !gjson.ValidBytes(payload) {
		return payload
	}

	// Try to fetch cached reasoning_content. When empty (new conversation,
	// server restart), we still inject empty reasoning_content for ALL
	// assistant messages — DeepSeek requires the field to exist.
	reasoningContent := opencodeGoGetCachedReasoningContent(model, sessionID)

	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() || len(messages.Array()) == 0 {
		// Also check for Responses API format (input array)
		input := gjson.GetBytes(payload, "input")
		if input.Exists() && input.IsArray() && len(input.Array()) > 0 {
			return opencodeGoInjectInputArrayReasoning(payload, model, reasoningContent)
		}
		return payload
	}

	msgs := messages.Array()
	modified := false

	// Find the LAST assistant message that is missing reasoning_content
	// This is the message from the previous turn that needs injection.
	// DeepSeek checks ALL assistant messages including tool_call-only ones,
	// so we inject empty reasoning_content for tool_call messages as well.
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		role := msg.Get("role").String()
		if role != "assistant" {
			continue
		}
		// Skip if already has reasoning_content
		if msg.Get("reasoning_content").Exists() && msg.Get("reasoning_content").String() != "" {
			continue
		}

		// For tool_call-only assistant messages, inject empty reasoning_content
		// to satisfy DeepSeek's thinking mode validation, then continue scanning
		// for a non-tool assistant message to inject the cached content into.
		isToolCall := msg.Get("tool_calls").Exists() && msg.Get("tool_calls").IsArray() &&
			len(msg.Get("tool_calls").Array()) > 0 && msg.Get("content").String() == ""

		content := reasoningContent
		if isToolCall {
			content = ""
		}

		path := fmt.Sprintf("messages.%d.reasoning_content", i)
		var err error
		payload, err = sjson.SetBytes(payload, path, content)
		if err == nil {
			modified = true
		}
		// Continue scanning — DeepSeek checks ALL assistant messages.
	}

	if !modified {
		return payload
	}
	return payload
}

// opencodeGoInjectInputArrayReasoning handles the Responses API format (input array).
func opencodeGoInjectInputArrayReasoning(payload []byte, model, content string) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() || !input.IsArray() {
		return payload
	}
	items := input.Array()
	modified := false

	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		role := item.Get("role").String()
		if role != "assistant" {
			continue
		}
		if item.Get("reasoning_content").Exists() && item.Get("reasoning_content").String() != "" {
			continue
		}

		isToolCall := item.Get("tool_calls").Exists() && item.Get("tool_calls").IsArray() &&
			len(item.Get("tool_calls").Array()) > 0 && item.Get("content").String() == ""

		injectContent := content
		if isToolCall {
			injectContent = ""
		}

		path := fmt.Sprintf("input.%d.reasoning_content", i)
		var err error
		payload, err = sjson.SetBytes(payload, path, injectContent)
		if err == nil {
			modified = true
		}
		// Continue scanning.
	}
	if !modified {
		return payload
	}
	return payload
}

// opencodeGoWrapStreamCacheReasoning wraps a StreamResult's chunk channel to intercept
// and cache reasoning_content from streaming responses.
func opencodeGoWrapStreamCacheReasoning(result *cliproxyexecutor.StreamResult, model, sessionID string) *cliproxyexecutor.StreamResult {
	if result == nil || model == "" || sessionID == "" {
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		for chunk := range result.Chunks {
			if chunk.Err == nil && len(chunk.Payload) > 0 {
				opencodeGoCacheReasoningFromStreamChunk(chunk.Payload, model, sessionID)
			}
			out <- chunk
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}
}

// opencodeGoInjectMessagesArray is a convenience wrapper that injects reasoning_content
// into a Chat Completions format payload (messages array).
func opencodeGoInjectMessagesArray(payload []byte, model, sessionID string) []byte {
	return opencodeGoInjectReasoningContentIntoPayload(payload, model, sessionID)
}
