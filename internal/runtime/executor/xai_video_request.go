package executor

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Request shaping for /videos/generations.
//
// Same reasoning as shapeXAIImageRequest: xAI rejects the whole request when an
// argument it does not know is present, so the body is filtered to an allowlist
// rather than forwarded verbatim. Callers coming from an OpenAI-shaped client
// routinely send size/quality/response_format, none of which exist here.
//
// The allowlist mirrors the SupportedParameters declared for the video models in
// internal/registry, so the catalog and the wire format cannot drift apart.

// xaiVideoRequestFields are the top-level keys xAI accepts on /videos/generations.
var xaiVideoRequestFields = map[string]struct{}{
	"model":  {},
	"prompt": {},
	// Image-to-video. Documented as an object with a "url" key, which may carry a
	// data URI as well as an https URL.
	"image": {},
	// Reference-to-video, mutually exclusive with "image" upstream.
	"reference_image_urls": {},
	"duration":             {},
	"aspect_ratio":         {},
	"resolution":           {},
	// Passed through for upstream abuse attribution.
	"user": {},
}

// shapeXAIVideoRequest removes fields the video endpoint rejects and normalizes the
// source image into the object form xAI documents.
//
// It returns the filtered body and the names it dropped, so the caller can tell the
// operator which of their settings were ignored rather than silently changing the
// meaning of the request.
func shapeXAIVideoRequest(payload []byte) ([]byte, []string) {
	parsed := gjson.ParseBytes(payload)
	if !parsed.IsObject() {
		return payload, nil
	}

	shaped := normalizeXAIVideoImageField(payload)
	dropped := make([]string, 0, 4)
	gjson.ParseBytes(shaped).ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		if _, ok := xaiVideoRequestFields[strings.ToLower(strings.TrimSpace(name))]; ok {
			return true
		}
		if next, err := sjson.DeleteBytes(shaped, escapeJSONPathKey(name)); err == nil {
			shaped = next
			dropped = append(dropped, name)
		}
		return true
	})

	if len(dropped) == 0 && len(shaped) == len(payload) {
		return payload, nil
	}
	return shaped, dropped
}

// normalizeXAIVideoImageField accepts the shapes clients actually send for the
// source image and rewrites them to the documented {"url": "..."} object.
//
// A bare string is what an OpenAI-shaped client and the xAI SDK both use
// (image_url), and a data URI is the only way to animate a locally uploaded file
// without a public URL. Rejecting those would make image-to-video reachable only
// for images already hosted somewhere.
func normalizeXAIVideoImageField(payload []byte) []byte {
	if url := strings.TrimSpace(gjson.GetBytes(payload, "image_url").String()); url != "" {
		if !gjson.GetBytes(payload, "image").Exists() {
			if next, err := sjson.SetBytes(payload, "image.url", url); err == nil {
				payload = next
			}
		}
		if next, err := sjson.DeleteBytes(payload, "image_url"); err == nil {
			payload = next
		}
	}

	image := gjson.GetBytes(payload, "image")
	if image.Type == gjson.String {
		url := strings.TrimSpace(image.String())
		if url == "" {
			if next, err := sjson.DeleteBytes(payload, "image"); err == nil {
				return next
			}
			return payload
		}
		if next, err := sjson.SetBytes(payload, "image", map[string]any{"url": url}); err == nil {
			return next
		}
	}
	return payload
}
