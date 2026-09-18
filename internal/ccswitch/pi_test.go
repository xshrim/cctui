package ccswitch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiProviderUsesNativeFilesAndPreservesProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CC_SWITCH_TEST_HOME", home)
	piDir := filepath.Join(home, ".pi", "agent")
	t.Setenv("PI_CODING_AGENT_DIR", piDir)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first, switched, err := store.AddProvider(AppPi, ProviderInput{Name: "Relay One", BaseURL: "https://one.example/v1", APIKey: "one-secret", Model: "model-one"})
	if err != nil || !switched {
		t.Fatalf("AddProvider() = %#v, %v, switched=%v", first, err, switched)
	}
	second, _, err := store.AddProvider(AppPi, ProviderInput{Name: "Relay Two", BaseURL: "https://two.example/v1", APIKey: "two-secret", Model: "model-two"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SwitchProvider(AppPi, second.ID); err != nil {
		t.Fatal(err)
	}
	models, err := os.ReadFile(filepath.Join(piDir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var modelsDoc map[string]any
	if err := json.Unmarshal(models, &modelsDoc); err != nil {
		t.Fatal(err)
	}
	providers := modelsDoc["providers"].(map[string]any)
	if len(providers) != 2 || providers[second.ID] == nil || providers[first.ID] == nil {
		t.Fatalf("providers = %#v", providers)
	}
	settings, _ := os.ReadFile(filepath.Join(piDir, "settings.json"))
	if !strings.Contains(string(settings), `"defaultProvider": "`+second.ID+`"`) {
		t.Fatalf("default provider not switched: %s", settings)
	}
}

func TestPiPreviewRedactsAPIKey(t *testing.T) {
	t.Setenv("CC_SWITCH_TEST_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider, _, err := store.AddProvider(AppPi, ProviderInput{Name: "Pi", APIKey: "pi-secret", Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.PreviewSwitch(AppPi, provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range preview.DiffLines() {
		if strings.Contains(line, "pi-secret") {
			t.Fatalf("preview leaked API key: %s", line)
		}
	}
}
