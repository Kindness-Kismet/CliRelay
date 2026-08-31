// Package openai implements thinking configuration for OpenAI/Codex models.
//
// OpenAI models use the reasoning_effort format with discrete levels
// (low/medium/high). Some models support xhigh and none levels.
// See: _bmad-output/planning-artifacts/architecture.md#Epic-8
package openai

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for OpenAI models.
//
// OpenAI-specific behavior:
//   - Output format: reasoning_effort (string: low/medium/high/xhigh)
//   - Level-only mode: no numeric budget support
//   - Some models support ZeroAllowed (gpt-5.1, gpt-5.2)
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new OpenAI thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("openai", NewApplier())
}

// Apply applies thinking configuration to OpenAI request body.
//
// Expected output format:
//
//	{
//	  "reasoning_effort": "high"
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyCompatibleOpenAI(body, config)
	}
	if modelInfo.Thinking == nil {
		// A model that declares no thinking support still has to be protected
		// from a value written upstream of here: the Claude→OpenAI translator
		// turns `thinking.type: "disabled"` into `reasoning_effort: "none"`
		// before this applier runs, and returning the body untouched forwards
		// that value to an upstream that never agreed to accept it.
		return dropNonStandardEffort(body), nil
	}

	// Only handle ModeLevel and ModeNone; other modes pass through unchanged.
	if config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	if config.Mode == thinking.ModeLevel {
		result, _ := sjson.SetBytes(body, "reasoning_effort", string(config.Level))
		return result, nil
	}

	// ModeNone: only wire "none" when the model actually accepts it.
	// ZeroAllowed alone must not force "none" when Levels omit "none";
	// fall back to lowest level so upstream does not 400 on "none".
	support := modelInfo.Thinking
	effort := ""
	if supportsNoneEffort(support) {
		effort = string(thinking.LevelNone)
	} else if config.Level != "" {
		effort = string(config.Level)
	} else if len(support.Levels) > 0 {
		effort = support.Levels[0]
	}
	if effort == "" {
		result, err := sjson.DeleteBytes(body, "reasoning_effort")
		if err != nil {
			return body, nil
		}
		return result, nil
	}
	result, _ := sjson.SetBytes(body, "reasoning_effort", effort)
	return result, nil
}

// supportsNoneEffort reports whether the model accepts reasoning_effort="none".
// Explicit Levels win: if the list omits "none", do not emit it even when ZeroAllowed.
func supportsNoneEffort(support *registry.ThinkingSupport) bool {
	if support == nil {
		return false
	}
	if hasLevel(support.Levels, string(thinking.LevelNone)) {
		return true
	}
	return support.ZeroAllowed && len(support.Levels) == 0
}

func applyCompatibleOpenAI(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	var effort string
	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		effort = string(config.Level)
	case thinking.ModeNone:
		// Omitting the field is how the OpenAI wire format says "do not think".
		// Sending "none" instead is what made Command Code answer
		// 400 `expected one of "low"|"medium"|"high"|"xhigh"|"max"` on every
		// Claude Code request that disabled thinking. The model-aware path in
		// Apply already refuses to emit "none" unless the model declares it;
		// a model we know nothing about deserves at least the same caution.
		effort = string(config.Level)
	case thinking.ModeAuto:
		// "auto" is an internal marker for "let the model decide", not a wire
		// value any OpenAI-compatible upstream accepts.
		effort = ""
	case thinking.ModeBudget:
		// Budget mode: convert budget to level using threshold mapping.
		// A zero budget maps to "none" and a negative one to "auto", so the
		// result goes through the same guard as the explicit modes.
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		effort = level
	default:
		return body, nil
	}

	if effort == "" || isNonStandardEffort(effort) {
		return dropNonStandardEffort(body), nil
	}

	result, _ := sjson.SetBytes(body, "reasoning_effort", effort)
	return result, nil
}

// isNonStandardEffort reports whether a reasoning_effort value is one this
// codebase uses internally rather than one the OpenAI wire format defines.
//
// "none" and "auto" are both internal markers. Upstreams differ on them —
// opencode go tolerates "none" while Command Code rejects it — so neither may
// be sent to a model whose accepted levels are unknown.
func isNonStandardEffort(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(thinking.LevelNone), string(thinking.LevelAuto):
		return true
	default:
		return false
	}
}

// dropNonStandardEffort removes a reasoning_effort the upstream would reject,
// leaving any legitimate value in place.
func dropNonStandardEffort(body []byte) []byte {
	effort := gjson.GetBytes(body, "reasoning_effort")
	if !effort.Exists() || !isNonStandardEffort(effort.String()) {
		return body
	}
	result, err := sjson.DeleteBytes(body, "reasoning_effort")
	if err != nil {
		return body
	}
	return result
}

func hasLevel(levels []string, target string) bool {
	for _, level := range levels {
		if strings.EqualFold(strings.TrimSpace(level), target) {
			return true
		}
	}
	return false
}
