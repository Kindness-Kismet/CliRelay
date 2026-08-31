package registry

import "strings"

// Video-generation model classification.
//
// Kept apart from image_generation_models.go on purpose: the two share the
// "grok-imagine-" family prefix but nothing else. Video is billed per call like
// images, but it is asynchronous (submit, then poll a request id), takes a
// different upstream path, and accepts a different argument set. Folding it into
// the image classifier would make every image code path — pricing, catalog
// modality, the /images/* endpoints — start accepting a model it cannot serve.

// videoGenerationModelDefaults describes a video model's billing and capability
// shape.
type videoGenerationModelDefaults struct {
	// PricePerCall is the per-invocation price. Left at zero for Grok Imagine for
	// the same reason as the image models: xAI bills these against the
	// subscription rather than at a published per-video rate, and an invented
	// number would surface as real spend in usage reporting.
	PricePerCall float64
	Description  string
	// MaxDurationSeconds is the longest clip the model accepts.
	MaxDurationSeconds int
}

// videoGenerationModels maps a model ID to its defaults. IDs are lower-cased.
var videoGenerationModels = map[string]videoGenerationModelDefaults{
	"grok-imagine-video-1.5": {
		Description:        "Grok Imagine video generation from text or a source image",
		MaxDurationSeconds: 15,
	},
}

// videoGenerationModelPrefixes covers releases that ship faster than this list is
// updated: a new grok-imagine-video-* revision is classified on day one.
var videoGenerationModelPrefixes = []string{
	"grok-imagine-video",
}

// IsVideoGenerationModel reports whether a model produces video.
func IsVideoGenerationModel(modelID string) bool {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	if normalized == "" {
		return false
	}
	if _, ok := videoGenerationModels[normalized]; ok {
		return true
	}
	for _, prefix := range videoGenerationModelPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// VideoGenerationModelDefaults returns the per-call price and description for a
// video model. The second result is false for anything that is not a known video
// model, including ones matched only by prefix, which have no published price.
func VideoGenerationModelDefaults(modelID string) (float64, string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	defaults, ok := videoGenerationModels[normalized]
	if !ok {
		return 0, "", false
	}
	return defaults.PricePerCall, defaults.Description, true
}

// VideoGenerationOutputModalities is the output modality set every video model
// carries. Returned as a fresh slice so callers can mutate it safely.
func VideoGenerationOutputModalities() []string {
	return []string{"video"}
}

// VideoGenerationInputModalities lists what a video model accepts. Both entries
// are real: the same endpoint serves text-to-video and image-to-video, selected by
// whether the caller supplies a source image.
func VideoGenerationInputModalities(string) []string {
	return []string{"text", "image"}
}

// VideoGenerationProvider returns the credential provider that serves a model, or
// "" when the model is not a video-generation model.
func VideoGenerationProvider(modelID string) string {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	if strings.HasPrefix(normalized, "grok-") && IsVideoGenerationModel(normalized) {
		return ImageProviderXAI
	}
	return ""
}

// VideoGenerationModel describes a selectable video model for the console.
type VideoGenerationModel struct {
	ID                 string  `json:"id"`
	Provider           string  `json:"provider"`
	DisplayName        string  `json:"display_name,omitempty"`
	Description        string  `json:"description,omitempty"`
	SupportsImage      bool    `json:"supports_image_to_video"`
	MaxDurationSeconds int     `json:"max_duration_seconds,omitempty"`
	PricePerCall       float64 `json:"price_per_call,omitempty"`
}

// ListVideoGenerationModels enumerates the video models this build knows about,
// derived from the static catalog so a newly registered model needs no second edit
// here to become selectable.
func ListVideoGenerationModels() []VideoGenerationModel {
	seen := make(map[string]struct{})
	models := make([]VideoGenerationModel, 0, 2)

	for _, info := range GetXAIModels() {
		if info == nil || !IsVideoGenerationModel(info.ID) {
			continue
		}
		if _, ok := seen[info.ID]; ok {
			continue
		}
		provider := VideoGenerationProvider(info.ID)
		if provider == "" {
			continue
		}
		seen[info.ID] = struct{}{}
		price, description, _ := VideoGenerationModelDefaults(info.ID)
		if strings.TrimSpace(info.Description) != "" {
			description = info.Description
		}
		maxDuration := 0
		if defaults, ok := videoGenerationModels[strings.ToLower(info.ID)]; ok {
			maxDuration = defaults.MaxDurationSeconds
		}
		models = append(models, VideoGenerationModel{
			ID:                 info.ID,
			Provider:           provider,
			DisplayName:        info.DisplayName,
			Description:        description,
			SupportsImage:      SupportsImageToVideo(info.ID),
			MaxDurationSeconds: maxDuration,
			PricePerCall:       price,
		})
	}
	return models
}

// SupportsImageToVideo reports whether a model accepts a source image to animate,
// as opposed to text-to-video only.
func SupportsImageToVideo(modelID string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(modelID)), "grok-imagine-video")
}
