package ccswitch

import (
	"strings"
	"testing"
)

func TestPreviewSwitchRedactsSecretsAndShowsChanges(t *testing.T) {
	t.Setenv("CC_SWITCH_TEST_HOME", t.TempDir())

	store, err := OpenStore()
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	if _, _, err := store.AddProvider(AppClaude, ProviderInput{
		Name:    "Current",
		BaseURL: "https://old.example.com",
		APIKey:  "old-secret",
		Model:   "claude-sonnet-old",
	}); err != nil {
		t.Fatalf("AddProvider(current) error = %v", err)
	}
	target, _, err := store.AddProvider(AppClaude, ProviderInput{
		Name:    "Target",
		BaseURL: "https://new.example.com",
		APIKey:  "new-secret",
		Model:   "claude-sonnet-new",
	})
	if err != nil {
		t.Fatalf("AddProvider(target) error = %v", err)
	}

	preview, err := store.PreviewSwitch(AppClaude, target.ID)
	if err != nil {
		t.Fatalf("PreviewSwitch() error = %v", err)
	}
	diff := strings.Join(preview.DiffLines(), "\n")

	if !strings.Contains(diff, "https://new.example.com") {
		t.Fatalf("diff does not contain the target URL: %s", diff)
	}
	if strings.Contains(diff, "old-secret") || strings.Contains(diff, "new-secret") {
		t.Fatalf("diff leaked an API key: %s", diff)
	}
	if !strings.Contains(diff, "[REDACTED]") {
		t.Fatalf("diff does not show redacted secret values: %s", diff)
	}
}

func TestDiffPreviewLinesTracksAdditionsAndRemovals(t *testing.T) {
	lines, added, removed := diffPreviewLines("a\nb\n", "a\nc\n")

	if added != 1 || removed != 1 {
		t.Fatalf("diff counts = +%d -%d, want +1 -1", added, removed)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "- b") || !strings.Contains(joined, "+ c") {
		t.Fatalf("diff lines = %q", joined)
	}
}
