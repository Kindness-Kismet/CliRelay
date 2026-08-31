package executor

// Command Code speaks plain OpenAI on https://api.commandcode.ai/provider/v1, so
// it is served by OpenAICompatExecutor rather than a dedicated executor. What
// lives here is only the provider identity and the per-credential lookups that
// the shared compat paths need.
//
// Note the plan boundary: Command Code's $1 "Go" plan mints a working key but
// answers 403 upgrade_required on this base URL, because that plan excludes API
// access. Every plan above it (GOAT and up) is served normally. The refusal is
// an entitlement signal, not a credential fault, so it is surfaced to the
// operator as-is instead of being retried or masked.

const commandCodeProvider = "commandcode"
