package grok

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSwitchToModelUpdatesSessionSummaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions", "cwd", "session", "summary.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"info": map[string]any{"current_model_id": "old-model"}})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := SwitchToModel(dir, "grok-4.6")
	if err != nil || report.Updated != 1 {
		t.Fatalf("SwitchToModel() = %#v, %v", report, err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err := json.Unmarshal(updated, &summary); err != nil {
		t.Fatal(err)
	}
	if summary["info"].(map[string]any)["current_model_id"] != "grok-4.6" {
		t.Fatalf("summary = %#v", summary)
	}
	if _, err := filepath.Glob(path + ".bak.session-restore-*"); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(path + ".bak.session-restore-*")
	if len(backups) != 1 {
		t.Fatalf("backup count = %d, want 1", len(backups))
	}
}
