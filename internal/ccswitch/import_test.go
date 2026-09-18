package ccswitch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseImportDocumentByExtension(t *testing.T) {
	jsonProviders, err := parseImportDocument("providers.json", []byte(`{
  "providers": [{"app": "claude", "name": "JSON", "api_key": "key"}]
}`))
	if err != nil {
		t.Fatalf("parse JSON error = %v", err)
	}
	if len(jsonProviders) != 1 || jsonProviders[0].Name != "JSON" {
		t.Fatalf("JSON providers = %#v", jsonProviders)
	}

	yamlProviders, err := parseImportDocument("providers.yaml", []byte("providers:\n  - app: gemini\n    name: YAML\n    api_key: yaml-key\n"))
	if err != nil {
		t.Fatalf("parse YAML error = %v", err)
	}
	if len(yamlProviders) != 1 || yamlProviders[0].Name != "YAML" {
		t.Fatalf("YAML providers = %#v", yamlProviders)
	}

	if _, err := parseImportDocument("providers.txt", []byte("{}")); err == nil {
		t.Fatal("parse unsupported extension succeeded")
	}

	listProviders, err := parseImportDocument("providers.yml", []byte("- app: codex\n  name: List\n"))
	if err != nil || len(listProviders) != 1 || listProviders[0].Name != "List" {
		t.Fatalf("top-level YAML list = %#v, error = %v", listProviders, err)
	}
}

func TestImportProviderFileSupportsGroupedYAML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CC_SWITCH_TEST_HOME", home)
	path := filepath.Join(home, "providers.yml")
	content := []byte("claude:\n  - name: Claude Relay\n    base_url: https://example.com\n    api_key: secret\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	result, err := store.ImportProviderFile(path)
	if err != nil {
		t.Fatalf("ImportProviderFile() error = %v", err)
	}
	if len(result.Imported) != 1 || len(result.Failed) != 0 {
		t.Fatalf("ImportProviderFile() result = %#v", result)
	}
	providers, err := store.ListProviders(AppClaude)
	if err != nil {
		t.Fatalf("ListProviders() error = %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("Claude provider count = %d, want 1", len(providers))
	}
}

func TestExportProviderFileCanBeImportedAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CC_SWITCH_TEST_HOME", home)
	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	if _, _, err := store.AddProvider(AppCodex, ProviderInput{
		Name:            "Codex Export",
		BaseURL:         "https://example.com/v1",
		APIKey:          "secret-key",
		Model:           "gpt-5.6",
		ReasoningEffort: "high",
		Website:         "https://example.com",
		Notes:           "export test",
	}); err != nil {
		t.Fatalf("AddProvider() error = %v", err)
	}

	path := filepath.Join(home, "export.yaml")
	count, err := store.ExportProviderFile(path, ExportOptions{})
	if err != nil || count != 1 {
		t.Fatalf("ExportProviderFile() = count %d, error %v", count, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), "secret-key") {
		t.Fatalf("export does not contain the API key: %s", data)
	}

	redactedPath := filepath.Join(home, "redacted.json")
	if _, err := store.ExportProviderFile(redactedPath, ExportOptions{RedactSecrets: true}); err != nil {
		t.Fatalf("redacted ExportProviderFile() error = %v", err)
	}
	redacted, err := os.ReadFile(redactedPath)
	if err != nil {
		t.Fatalf("ReadFile(redacted) error = %v", err)
	}
	if strings.Contains(string(redacted), "secret-key") || !strings.Contains(string(redacted), "[REDACTED]") {
		t.Fatalf("redacted export is incorrect: %s", redacted)
	}
}

func TestImportProviderFileUpdatesSameNameAndKeepsOtherProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CC_SWITCH_TEST_HOME", home)
	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	if _, _, err := store.AddProvider(AppClaude, ProviderInput{
		Name: "Relay", BaseURL: "https://old.example.com", APIKey: "old-key", Model: "old-model",
	}); err != nil {
		t.Fatalf("AddProvider(Relay) error = %v", err)
	}
	if _, _, err := store.AddProvider(AppClaude, ProviderInput{
		Name: "Keep Me", BaseURL: "https://keep.example.com", APIKey: "keep-key", Model: "keep-model",
	}); err != nil {
		t.Fatalf("AddProvider(Keep Me) error = %v", err)
	}

	path := filepath.Join(home, "providers.json")
	content := []byte(`{"providers":[{"app":"claude","name":"relay","base_url":"https://new.example.com","api_key":"new-key","model":"new-model"},{"app":"claude","name":"Added","base_url":"https://added.example.com"}]}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	result, err := store.ImportProviderFile(path)
	if err != nil {
		t.Fatalf("ImportProviderFile() error = %v", err)
	}
	if len(result.Updated) != 1 || len(result.Imported) != 1 {
		t.Fatalf("result = %#v, want one update and one append", result)
	}

	providers, err := store.ListProviders(AppClaude)
	if err != nil {
		t.Fatalf("ListProviders() error = %v", err)
	}
	if len(providers) != 3 {
		t.Fatalf("provider count = %d, want 3", len(providers))
	}
	relay := providerByName(providers, "Relay")
	if relay == nil {
		t.Fatal("updated Relay provider not found")
	}
	env := relay.SettingsConfig["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "https://new.example.com" {
		t.Fatalf("Relay base URL = %v, want updated URL", env["ANTHROPIC_BASE_URL"])
	}
	if providerByName(providers, "Keep Me") == nil || providerByName(providers, "Added") == nil {
		t.Fatalf("existing or newly appended provider was lost: %#v", providers)
	}
}
