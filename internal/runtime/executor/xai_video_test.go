package executor

import (
	"strings"
	"testing"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/xai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestShapeXAIVideoRequestDropsUnsupportedArguments(t *testing.T) {
	payload := []byte(`{"model":"grok-imagine-video-1.5","prompt":"a cat","duration":10,"aspect_ratio":"16:9","resolution":"720p","size":"1024x1024","quality":"high","response_format":"b64_json"}`)

	shaped, dropped := shapeXAIVideoRequest(payload)

	for _, field := range []string{"size", "quality", "response_format"} {
		if gjson.GetBytes(shaped, field).Exists() {
			t.Fatalf("%s must be dropped: %s", field, shaped)
		}
	}
	for _, field := range []string{"model", "prompt", "duration", "aspect_ratio", "resolution"} {
		if !gjson.GetBytes(shaped, field).Exists() {
			t.Fatalf("%s must be preserved: %s", field, shaped)
		}
	}
	if len(dropped) != 3 {
		t.Fatalf("dropped = %v, want the three unsupported arguments", dropped)
	}
}

// The console and OpenAI-shaped clients send a bare string or image_url; xAI only
// accepts {"image":{"url":...}}. Normalizing keeps image-to-video reachable
// without asking every caller to know the upstream's exact shape.
func TestShapeXAIVideoRequestNormalizesSourceImage(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"bare string", `{"model":"grok-imagine-video-1.5","prompt":"go","image":"https://example.com/a.png"}`},
		{"image_url alias", `{"model":"grok-imagine-video-1.5","prompt":"go","image_url":"https://example.com/a.png"}`},
		{"already an object", `{"model":"grok-imagine-video-1.5","prompt":"go","image":{"url":"https://example.com/a.png"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shaped, _ := shapeXAIVideoRequest([]byte(tc.payload))
			if got := gjson.GetBytes(shaped, "image.url").String(); got != "https://example.com/a.png" {
				t.Fatalf("image.url = %q, want the source url; body=%s", got, shaped)
			}
			if gjson.GetBytes(shaped, "image_url").Exists() {
				t.Fatalf("image_url alias must not reach the upstream: %s", shaped)
			}
		})
	}
}

func TestShapeXAIVideoRequestKeepsDataURISourceImage(t *testing.T) {
	const dataURI = "data:image/png;base64,aGVsbG8="
	shaped, _ := shapeXAIVideoRequest([]byte(`{"model":"grok-imagine-video-1.5","prompt":"go","image":"` + dataURI + `"}`))
	if got := gjson.GetBytes(shaped, "image.url").String(); got != dataURI {
		t.Fatalf("image.url = %q, want the data uri intact", got)
	}
}

func TestXAIVideoRequestIDRejectsPathEscapes(t *testing.T) {
	valid := []string{"d97415a1-5796-b7ec-379f-4e6819e08fdf", "abc123", "a_b-c"}
	for _, id := range valid {
		if !xaiIsSafeVideoRequestID(id) {
			t.Fatalf("%q must be accepted", id)
		}
	}
	invalid := []string{"", "../secrets", "a/b", "a?b=1", "a b", strings.Repeat("a", 129)}
	for _, id := range invalid {
		if xaiIsSafeVideoRequestID(id) {
			t.Fatalf("%q must be rejected", id)
		}
	}
}

// Video shares the media host rule: subscription credentials chat through the CLI
// gateway, which does not serve these paths, so media leaves for the official API.
func TestXAIVideoEndpointUsesMediaHost(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": xaiauth.CLIChatProxyBaseURL}}

	generations, err := xaiVideoEndpointFor(auth, xaiVideoGenerationAlt, "")
	if err != nil {
		t.Fatalf("generations endpoint: %v", err)
	}
	if !strings.HasPrefix(generations, xaiauth.DefaultAPIBaseURL) || !strings.HasSuffix(generations, "/videos/generations") {
		t.Fatalf("generations endpoint = %q", generations)
	}

	status, err := xaiVideoEndpointFor(auth, xaiVideoStatusAlt, "req-1")
	if err != nil {
		t.Fatalf("status endpoint: %v", err)
	}
	if !strings.HasSuffix(status, "/videos/req-1") {
		t.Fatalf("status endpoint = %q", status)
	}

	if _, err := xaiVideoEndpointFor(auth, xaiVideoStatusAlt, "../models"); err == nil {
		t.Fatal("a malformed request id must not produce an endpoint")
	}
}
