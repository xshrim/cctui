package ccswitch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeConfigPathEnvironmentVariablesTakePrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CC_SWITCH_TEST_HOME", home)
	claudeDir := filepath.Join(home, "claude-native")
	codexDir := filepath.Join(home, "codex-native")
	geminiDir := filepath.Join(home, "gemini-native")
	opencodeDir := filepath.Join(home, "opencode-native")
	opencodeFile := filepath.Join(home, "custom-opencode.jsonc")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", codexDir)
	t.Setenv("GEMINI_CLI_HOME", geminiDir)
	t.Setenv("OPENCODE_CONFIG_DIR", opencodeDir)
	t.Setenv("OPENCODE_CONFIG", opencodeFile)

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	if got := store.ConfigDir(AppClaude); got != claudeDir {
		t.Fatalf("Claude config dir = %q, want %q", got, claudeDir)
	}
	if got := store.ConfigDir(AppCodex); got != codexDir {
		t.Fatalf("Codex config dir = %q, want %q", got, codexDir)
	}
	if got := store.ConfigDir(AppGemini); got != geminiDir {
		t.Fatalf("Gemini config dir = %q, want %q", got, geminiDir)
	}
	if got := store.ConfigDir(AppOpencode); got != opencodeDir {
		t.Fatalf("OpenCode config dir = %q, want %q", got, opencodeDir)
	}
	if got := store.OpenCodeConfigPath(); got != opencodeFile {
		t.Fatalf("OpenCode config path = %q, want %q", got, opencodeFile)
	}
}

func TestXDGConfigHomeIsUsedForOpenCodeFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CC_SWITCH_TEST_HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("OPENCODE_CONFIG", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	xdg := filepath.Join(home, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	want := filepath.Join(xdg, "opencode")
	if got := store.ConfigDir(AppOpencode); got != want {
		t.Fatalf("OpenCode config dir = %q, want %q", got, want)
	}
}

func TestSwitchProviderRejectsExternalLiveChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CC_SWITCH_TEST_HOME", home)
	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	if _, _, err := store.AddProvider(AppClaude, ProviderInput{
		Name: "Current", APIKey: "current-key", Model: "current-model",
	}); err != nil {
		t.Fatalf("AddProvider(current) error = %v", err)
	}
	target, _, err := store.AddProvider(AppClaude, ProviderInput{
		Name: "Target", APIKey: "target-key", Model: "target-model",
	})
	if err != nil {
		t.Fatalf("AddProvider(target) error = %v", err)
	}

	path := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := store.SwitchProvider(AppClaude, target.ID); err == nil {
		t.Fatal("SwitchProvider() succeeded after an external live change")
	}
}

func TestDetectGeminiAuthModeDoesNotTreatAnyEnvAsAPIKey(t *testing.T) {
	settings := map[string]any{
		"env": map[string]any{
			"GOOGLE_CLOUD_PROJECT": "project-id",
		},
	}
	if got := detectGeminiAuthMode(settings); got != geminiAuthModeVertex {
		t.Fatalf("detectGeminiAuthMode() = %q, want %q", got, geminiAuthModeVertex)
	}
}

func TestUpdateClaudePreservesIndependentDefaultModels(t *testing.T) {
	existing := Provider{
		ID:   "claude",
		Name: "Claude",
		SettingsConfig: map[string]any{
			"env": map[string]any{
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "sonnet-model",
				"ANTHROPIC_DEFAULT_OPUS_MODEL":   "opus-model",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "haiku-model",
			},
		},
		Meta: map[string]any{"apiKeyField": "ANTHROPIC_API_KEY"},
	}
	updated, err := (&Store{}).buildProvider(AppClaude, &existing, ProviderInput{
		Name:              "Claude",
		APIKey:            "new-key",
		ClaudeAPIKeyField: "ANTHROPIC_API_KEY",
		Model:             "sonnet-model",
	}, existing.ID)
	if err != nil {
		t.Fatalf("buildProvider() error = %v", err)
	}
	env := updated.SettingsConfig["env"].(map[string]any)
	if env["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "opus-model" || env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "haiku-model" {
		t.Fatalf("independent Claude models were changed: %#v", env)
	}
}
