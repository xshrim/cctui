package ccswitch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCodeProvidersUseDistinctKeysAndPreserveEntryFields(t *testing.T) {
	t.Setenv("CC_SWITCH_TEST_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	first, _, err := store.AddProvider(AppOpencode, ProviderInput{
		Name:    "OpenAI",
		BaseURL: "https://one.example.com",
		APIKey:  "one-key",
		Model:   "model-one",
	})
	if err != nil {
		t.Fatalf("AddProvider(first) error = %v", err)
	}
	second, _, err := store.AddProvider(AppOpencode, ProviderInput{
		Name:    "OpenAI",
		BaseURL: "https://two.example.com",
		APIKey:  "two-key",
		Model:   "model-two",
	})
	if err != nil {
		t.Fatalf("AddProvider(second) error = %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("duplicate OpenCode provider IDs: %q", first.ID)
	}

	stored, err := store.GetProvider(AppOpencode, first.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetProvider(first) = %#v, %v", stored, err)
	}
	entry := stored.SettingsConfig["provider"].(map[string]any)[first.ID].(map[string]any)
	entry["npm"] = "@example/openai-provider"
	entry["customField"] = "preserve-me"
	entry["models"].(map[string]any)["extra-model"] = map[string]any{
		"name": "Extra Model",
	}
	if _, err := store.UpdateProvider(AppOpencode, *stored, ProviderInput{
		Name:    stored.Name,
		BaseURL: "https://one-updated.example.com",
		APIKey:  "one-updated-key",
		Model:   "model-one-updated",
	}); err != nil {
		t.Fatalf("UpdateProvider() error = %v", err)
	}

	settings, err := os.ReadFile(store.opencodeConfigPath())
	if err != nil {
		t.Fatalf("ReadFile(opencode.json) error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(settings, &doc); err != nil {
		t.Fatalf("decode opencode.json error = %v", err)
	}
	providers := doc["provider"].(map[string]any)
	if len(providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(providers))
	}
	updatedEntry := providers[first.ID].(map[string]any)
	if updatedEntry["npm"] != "@example/openai-provider" || updatedEntry["customField"] != "preserve-me" {
		t.Fatalf("provider-level fields were lost: %#v", updatedEntry)
	}
	if _, ok := updatedEntry["models"].(map[string]any)["extra-model"]; !ok {
		t.Fatalf("extra model was lost: %#v", updatedEntry["models"])
	}
}

func TestBootstrapImportsEachOpenCodeProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CC_SWITCH_TEST_HOME", home)
	configDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	config := map[string]any{
		"provider": map[string]any{
			"alpha": map[string]any{"models": map[string]any{"a": map[string]any{"name": "a"}}},
			"beta":  map[string]any{"models": map[string]any{"b": map[string]any{"name": "b"}}},
		},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	warnings, err := store.Bootstrap()
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	_ = warnings

	providers, err := store.ListProviders(AppOpencode)
	if err != nil {
		t.Fatalf("ListProviders() error = %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(providers))
	}
	for _, provider := range providers {
		if opencodeProviderKeyForProvider(provider) != provider.ID {
			t.Fatalf("provider %q key metadata = %q", provider.ID, opencodeProviderKeyForProvider(provider))
		}
	}
}

func TestUnmarshalOpenCodeConfigAcceptsJSONC(t *testing.T) {
	var config map[string]any
	err := unmarshalOpenCodeConfig([]byte(`{
  // keep provider config readable
  "provider": {
    "demo": {"models": {},},
  },
}`), &config)
	if err != nil {
		t.Fatalf("unmarshalOpenCodeConfig() error = %v", err)
	}
	if _, ok := config["provider"].(map[string]any); !ok {
		t.Fatalf("provider section type = %T", config["provider"])
	}
}
