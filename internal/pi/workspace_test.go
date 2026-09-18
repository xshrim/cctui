package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSwitchToModelAppendsModelChangeAndBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "--cwd--", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		`{"type":"session","version":3,"id":"session"}`,
		`{"type":"message","id":"abcd1234","parentId":null,"message":{"role":"user","content":"hi"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := SwitchToModel(dir, "relay", "model-a")
	if err != nil || report.Updated != 1 {
		t.Fatalf("SwitchToModel() = %#v, %v", report, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var change map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &change); err != nil {
		t.Fatal(err)
	}
	if change["type"] != "model_change" || change["provider"] != "relay" || change["modelId"] != "model-a" || change["parentId"] != "abcd1234" {
		t.Fatalf("model change = %#v", change)
	}
	backups, _ := filepath.Glob(path + ".bak.session-restore-*")
	if len(backups) != 1 {
		t.Fatalf("backup count = %d, want 1", len(backups))
	}
}
