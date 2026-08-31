package registry

import "testing"

func TestVideoModelsAreClassifiedAndServedByXAI(t *testing.T) {
	for _, id := range []string{"grok-imagine-video-1.5", "grok-imagine-video", "GROK-IMAGINE-VIDEO-2.0"} {
		if !IsVideoGenerationModel(id) {
			t.Errorf("%q must classify as a video generation model", id)
		}
		if provider := VideoGenerationProvider(id); provider != ImageProviderXAI {
			t.Errorf("%q provider = %q, want xai", id, provider)
		}
	}
}

// The two families share the "grok-imagine-" prefix but not a single code path:
// video is asynchronous, takes different arguments and a different upstream path.
func TestVideoAndImageModelsDoNotCrossClassify(t *testing.T) {
	if IsImageGenerationModel("grok-imagine-video-1.5") {
		t.Error("a video model must not classify as an image model")
	}
	for _, id := range []string{"grok-imagine-image", "gpt-image-2", "grok-4.5", ""} {
		if IsVideoGenerationModel(id) {
			t.Errorf("%q must not classify as a video model", id)
		}
		if provider := VideoGenerationProvider(id); provider != "" {
			t.Errorf("%q video provider = %q, want empty", id, provider)
		}
	}
}

// Screenshot-level regression: the console could not offer video at all because the
// catalog never carried the model.
func TestVideoModelIsListedFromTheStaticCatalog(t *testing.T) {
	models := ListVideoGenerationModels()
	if len(models) == 0 {
		t.Fatal("no video models listed; the console has nothing to select")
	}
	var found bool
	for _, model := range models {
		if model.ID != "grok-imagine-video-1.5" {
			continue
		}
		found = true
		if model.Provider != ImageProviderXAI {
			t.Errorf("provider = %q, want xai", model.Provider)
		}
		if !model.SupportsImage {
			t.Error("grok-imagine-video must advertise image-to-video")
		}
		if model.MaxDurationSeconds <= 0 {
			t.Error("max duration must be published so the console can bound the form")
		}
	}
	if !found {
		t.Fatalf("grok-imagine-video-1.5 missing from %+v", models)
	}
}

func TestVideoModalitiesCoverBothEntryModes(t *testing.T) {
	inputs := VideoGenerationInputModalities("grok-imagine-video-1.5")
	if len(inputs) != 2 || inputs[0] != "text" || inputs[1] != "image" {
		t.Fatalf("input modalities = %v, want text and image", inputs)
	}
	if outputs := VideoGenerationOutputModalities(); len(outputs) != 1 || outputs[0] != "video" {
		t.Fatalf("output modalities = %v, want video", outputs)
	}
}
