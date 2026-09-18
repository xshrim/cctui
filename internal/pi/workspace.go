package pi

import (
	"crypto/rand"
	"encoding/hex"
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

// SwitchToModel appends a model_change entry to each persistent Pi session.
// Existing files are backed up before the new entry is written.
func SwitchToModel(sessionDir, provider, model string) (*RepairReport, error) {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	report := &RepairReport{}
	err := filepath.WalkDir(sessionDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) == 0 || lines[0] == "" {
			report.Skipped++
			return nil
		}
		var header map[string]any
		if json.Unmarshal([]byte(lines[0]), &header) != nil || header["type"] != "session" {
			report.Skipped++
			return nil
		}
		lastID := ""
		lastProvider, lastModel := "", ""
		for _, line := range lines[1:] {
			var item map[string]any
			if json.Unmarshal([]byte(line), &item) != nil {
				continue
			}
			if id, ok := item["id"].(string); ok {
				lastID = id
			}
			if item["type"] == "model_change" {
				lastProvider, _ = item["provider"].(string)
				lastModel, _ = item["modelId"].(string)
			}
		}
		if lastProvider == provider && lastModel == model {
			return nil
		}
		idBytes := make([]byte, 4)
		if _, err := rand.Read(idBytes); err != nil {
			return err
		}
		modelEntry := map[string]any{
			"type": "model_change", "id": hex.EncodeToString(idBytes), "parentId": nil,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "provider": provider, "modelId": model,
		}
		if lastID != "" {
			modelEntry["parentId"] = lastID
		}
		encoded, err := json.Marshal(modelEntry)
		if err != nil {
			return err
		}
		backup := fmt.Sprintf("%s.bak.session-restore-%d", path, time.Now().UnixNano())
		if err := os.WriteFile(backup, data, 0o600); err != nil {
			return err
		}
		updated := append(append([]byte{}, data...), encoded...)
		if len(data) == 0 || data[len(data)-1] != '\n' {
			updated = append(updated[:len(updated)-len(encoded)], '\n')
			updated = append(updated, encoded...)
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
