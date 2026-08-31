package registry

// Video-generation model definitions.
//
// Separate from the image definitions for the same reason those are separate from
// the general catalog: the classifier in video_generation_models.go reads these,
// and the static catalog file is at its frozen line budget.

// getXAIVideoModelDefinitions returns Grok Imagine video-generation models.
//
// Like the image models, video requests reach the official API host even for
// subscription credentials, because the CLI gateway rejects the payload sizes and
// does not serve the media paths; see xaiMediaBaseURL in the runtime executor.
func getXAIVideoModelDefinitions() []*ModelInfo {
	return []*ModelInfo{
		{
			ID:          "grok-imagine-video-1.5",
			Object:      "model",
			OwnedBy:     "xai",
			Type:        "xai",
			Version:     "grok-imagine-video-1.5",
			DisplayName: "Grok Imagine Video",
			Name:        "grok-imagine-video-1.5",
			Description: "Grok Imagine text-to-video and image-to-video generation.",
			// Mirrors the argument allowlist in xaiVideoRequestFields; the catalog
			// and the wire format must not drift apart.
			SupportedParameters: []string{"prompt", "image", "reference_image_urls", "duration", "aspect_ratio", "resolution"},
		},
	}
}
