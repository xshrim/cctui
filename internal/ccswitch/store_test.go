package ccswitch

import (
	"strings"
	"testing"
)

func TestPatchCodexConfigDoesNotDisableResponseStorage(t *testing.T) {
	config, err := patchCodexConfig("", ProviderInput{
		BaseURL:         "https://api.example.com/v1",
		Model:           "gpt-5.6-terra",
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("patchCodexConfig() error = %v", err)
	}

	if strings.Contains(config, "disable_response_storage") {
		t.Fatalf("config unexpectedly disables response storage: %s", config)
	}
	if strings.Contains(config, "[model_providers]\n") {
		t.Fatalf("config contains an empty model_providers section: %s", config)
	}
	if !strings.Contains(config, "[model_providers.custom]") {
		t.Fatalf("config is missing the custom provider section: %s", config)
	}
}

func TestPatchCodexConfigRemovesLegacyResponseStorageSetting(t *testing.T) {
	existing := `disable_response_storage = true
model = 'old-model'
model_provider = 'custom'

[model_providers]
[model_providers.custom]
base_url = 'https://old.example.com/v1'
name = 'custom'
wire_api = 'responses'
requires_openai_auth = true

[projects."/tmp/project"]
trust_level = "trusted"
`

	config, err := patchCodexConfig(existing, ProviderInput{
		BaseURL: "https://api.example.com/v1",
		Model:   "gpt-5.6-terra",
	})
	if err != nil {
		t.Fatalf("patchCodexConfig() error = %v", err)
	}

	if strings.Contains(config, "disable_response_storage") {
		t.Fatalf("legacy response storage setting was not removed: %s", config)
	}
	if strings.Contains(config, "[model_providers]\n") {
		t.Fatalf("config contains an empty model_providers section: %s", config)
	}
	if !strings.Contains(config, "[model_providers.custom]") {
		t.Fatalf("custom provider section was lost: %s", config)
	}
	if !strings.Contains(config, "trust_level") {
		t.Fatalf("unrelated project configuration was lost: %s", config)
	}
}

func TestParseCodexConfigFieldsUsesActiveProviderTable(t *testing.T) {
	config := `model = "gpt-5.6"
model_provider = "second"
model_reasoning_effort = "high"

[model_providers.first]
base_url = "https://first.example.com/v1"

[model_providers.second]
openai_base_url = "https://second.example.com/v1"
`

	baseURL, model, effort, err := parseCodexConfigFields(config)
	if err != nil {
		t.Fatalf("parseCodexConfigFields() error = %v", err)
	}
	if baseURL != "https://second.example.com/v1" {
		t.Fatalf("baseURL = %q, want active provider URL", baseURL)
	}
	if model != "gpt-5.6" || effort != "high" {
		t.Fatalf("model fields = (%q, %q), want (gpt-5.6, high)", model, effort)
	}
}
