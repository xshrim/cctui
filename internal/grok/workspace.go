package grok

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type RepairReport struct {
	Updated int
	Skipped int
}

// SwitchToModel updates persisted Grok session metadata so resumed sessions
// use the selected model. Each changed summary is backed up before replacement.
func SwitchToModel(grokDir, modelID string) (*RepairReport, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return &RepairReport{}, nil
	}
	sessionsDir := filepath.Join(grokDir, "sessions")
	report := &RepairReport{}
	err := filepath.WalkDir(sessionsDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() || entry.Name() != "summary.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var summary map[string]any
		if err := json.Unmarshal(data, &summary); err != nil {
			report.Skipped++
			return nil
		}
		info, _ := summary["info"].(map[string]any)
		if info == nil {
			report.Skipped++
			return nil
		}
		if stringValue(info["current_model_id"]) == modelID {
			return nil
		}
		info["current_model_id"] = modelID
		updated, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return err
		}
		backup := fmt.Sprintf("%s.bak.session-restore-%d", path, time.Now().UnixNano())
		if err := os.WriteFile(backup, data, 0o600); err != nil {
			return err
		}
		if err := writeAtomic(path, updated); err != nil {
			return err
		}
		report.Updated++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func writeAtomic(path string, data []byte) error {
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
