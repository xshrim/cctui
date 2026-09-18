package ccswitch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrokProviderUsesOfficialConfigAndSwitches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CC_SWITCH_TEST_HOME", home)
	grokHome := filepath.Join(home, ".grok")
	t.Setenv("GROK_HOME", grokHome)
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first, switched, err := store.AddProvider(AppGrok, ProviderInput{
		Name: "Relay One", BaseURL: "https://one.example/v1", APIKey: "secret-one", Model: "grok-4.6",
	})
	if err != nil || !switched {
		t.Fatalf("AddProvider() = %#v, %v, switched=%v", first, err, switched)
	}
	second, _, err := store.AddProvider(AppGrok, ProviderInput{
		Name: "Relay Two", BaseURL: "https://two.example/v1", APIKey: "secret-two", Model: "grok-4.5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SwitchProvider(AppGrok, second.ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(grokHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "default = '"+second.ID+"'") || !strings.Contains(text, "base_url = 'https://two.example/v1'") {
		t.Fatalf("unexpected Grok config: %s", text)
	}
	if _, err := store.GetEffectiveCurrentProvider(AppGrok); err != nil {
		t.Fatal(err)
	}
}

func TestGrokPreviewRedactsAPIKey(t *testing.T) {
	t.Setenv("CC_SWITCH_TEST_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider, _, err := store.AddProvider(AppGrok, ProviderInput{Name: "Grok", APIKey: "super-secret", Model: "grok-4.6"})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.PreviewSwitch(AppGrok, provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range preview.DiffLines() {
		if strings.Contains(line, "super-secret") {
			t.Fatalf("preview leaked API key: %s", line)
		}
	}
}
