package handlers

import (
	"strings"

	"github.com/gin-gonic/gin"
)

func requestSessionStickyHeaderKey(c *gin.Context) string {
	if c == nil {
		return ""
	}
	// Prefer stable client session / conversation headers. Grok/xAI clients send
	// X-Grok-Session-Id / X-Grok-Conv-Id rather than generic Session-Id; without
	// them session-sticky collapses many concurrent chats onto one auth binding.
	// DeepSeek Harness likewise sends only X-Deepseek-Harness-Session-Id (per
	// turn, stable for the session) and no generic header or body session field:
	// missing it leaves the seed empty, so applySessionPromptCacheKey emits no
	// prompt_cache_key and the turn drifts across auth bindings, losing the
	// provider-side prefix cache that is scoped per upstream account.
	for _, header := range []string{
		"Session-Id",
		"session_id",
		"X-Session-Id",
		"X-Codex-Session-Id",
		"X-Claude-Code-Session-Id",
		"X-Grok-Session-Id",
		"X-Deepseek-Harness-Session-Id",
		"Conversation-Id",
		"conversation_id",
		"X-Conversation-Id",
		"OpenAI-Conversation-Id",
		"X-Grok-Conv-Id",
	} {
		if value := strings.TrimSpace(c.GetHeader(header)); value != "" {
			return "header:" + strings.ToLower(header) + ":" + value
		}
	}
	return ""
}
