package ccswitch

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// SwitchFileDiff contains the redacted diff for one live configuration file.
type SwitchFileDiff struct {
	Path    string
	Lines   []string
	Added   int
	Removed int
}

func (d SwitchFileDiff) Changed() bool {
	return d.Added > 0 || d.Removed > 0
}

// SwitchPreview is generated before a provider switch and contains the files
// that the switch would write. Secrets are redacted before producing diffs.
type SwitchPreview struct {
	App             AppType
	CurrentProvider string
	TargetProvider  string
	Files           []SwitchFileDiff
}

func (p SwitchPreview) DiffLines() []string {
	lines := make([]string, 0, len(p.Files)*4)
	for _, file := range p.Files {
		lines = append(lines, fmt.Sprintf("--- %s", file.Path))
		lines = append(lines, fmt.Sprintf("+++ %s", file.Path))
		lines = append(lines, file.Lines...)
	}
	return lines
}

// PreviewSwitch builds the exact live files that would be written by a
// replacement-mode provider switch, without changing any state.
func (s *Store) PreviewSwitch(app AppType, id string) (*SwitchPreview, error) {
	if app.IsIncremental() {
		return nil, errors.New("增量模式不支持供应商切换预览")
	}

	target, err := s.GetProvider(app, id)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, errors.New("供应商不存在")
	}

	currentID, err := s.GetEffectiveCurrentProvider(app)
	if err != nil {
		return nil, err
	}
	currentName := "未配置"
	if currentID != "" {
		current, err := s.GetProvider(app, currentID)
		if err != nil {
			return nil, err
		}
		if current != nil {
			currentName = current.Name
		}
	}

	targetFiles, err := s.previewTargetFiles(app, *target)
	if err != nil {
		return nil, err
	}

	preview := &SwitchPreview{
		App:             app,
		CurrentProvider: currentName,
		TargetProvider:  target.Name,
		Files:           make([]SwitchFileDiff, 0, len(targetFiles)),
	}
	for _, targetFile := range targetFiles {
		oldData, err := readPreviewFile(targetFile.path)
		if err != nil {
			return nil, err
		}

		oldText := normalizePreviewData(targetFile.kind, oldData)
		newText := normalizePreviewData(targetFile.kind, targetFile.data)
		lines, added, removed := diffPreviewLines(oldText, newText)
		preview.Files = append(preview.Files, SwitchFileDiff{
			Path:    targetFile.path,
			Lines:   lines,
			Added:   added,
			Removed: removed,
		})
	}

	return preview, nil
}

type previewFileKind int

const (
	previewJSON previewFileKind = iota
	previewEnv
	previewText
)

type previewFile struct {
	path string
	kind previewFileKind
	data []byte
}

func (s *Store) previewTargetFiles(app AppType, provider Provider) ([]previewFile, error) {
	switch app {
	case AppClaude:
		data, err := marshalPreviewJSON(provider.SettingsConfig)
		if err != nil {
			return nil, fmt.Errorf("生成 Claude 配置预览失败: %w", err)
		}
		return []previewFile{{path: s.claudeSettingsPath(), kind: previewJSON, data: data}}, nil

	case AppCodex:
		settings := CloneMap(provider.SettingsConfig)
		auth, ok := settings["auth"].(map[string]any)
		if !ok || auth == nil {
			auth = map[string]any{}
		}
		authData, err := marshalPreviewJSON(auth)
		if err != nil {
			return nil, fmt.Errorf("生成 Codex auth 配置预览失败: %w", err)
		}
		return []previewFile{
			{path: s.codexAuthPath(), kind: previewJSON, data: authData},
			{path: s.codexConfigPath(), kind: previewText, data: []byte(stringValue(settings["config"]))},
		}, nil

	case AppGemini:
		return s.previewGeminiFiles(provider)
	case AppGrok:
		settings := CloneMap(provider.SettingsConfig)
		config := getOrCreateMap(settings, "config")
		data, err := toml.Marshal(config)
		if err != nil {
			return nil, fmt.Errorf("生成 Grok 配置预览失败: %w", err)
		}
		return []previewFile{{path: s.grokConfigPath(), kind: previewText, data: data}}, nil
	default:
		return nil, fmt.Errorf("不支持的应用类型: %s", app)
	}
}

func (s *Store) previewGeminiFiles(provider Provider) ([]previewFile, error) {
	settings := CloneMap(provider.SettingsConfig)
	env := getOrCreateMap(settings, "env")
	envMap := map[string]string{}
	for key, value := range env {
		if text := strings.TrimSpace(stringValue(value)); text != "" {
			envMap[key] = text
		}
	}

	configDoc := map[string]any{}
	if content, err := os.ReadFile(s.geminiSettingsPath()); err == nil {
		if len(strings.TrimSpace(string(content))) > 0 {
			if err := json.Unmarshal(content, &configDoc); err != nil {
				return nil, fmt.Errorf("解析 Gemini settings.json 失败: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 Gemini settings.json 失败: %w", err)
	}
	if rawConfig, ok := settings["config"]; ok && rawConfig != nil {
		configMap, ok := rawConfig.(map[string]any)
		if !ok {
			return nil, errors.New("Gemini config 字段必须是对象或 null")
		}
		mergeMaps(configDoc, configMap)
	}

	selectedType := "gemini-api-key"
	if len(envMap) == 0 {
		selectedType = "oauth-personal"
	}
	setNestedMapValue(configDoc, []string{"security", "auth", "selectedType"}, selectedType)

	envData := []byte(serializeEnvFile(envMap))
	configData, err := marshalPreviewJSON(configDoc)
	if err != nil {
		return nil, fmt.Errorf("生成 Gemini settings 配置预览失败: %w", err)
	}
	return []previewFile{
		{path: s.geminiEnvPath(), kind: previewEnv, data: envData},
		{path: s.geminiSettingsPath(), kind: previewJSON, data: configData},
	}, nil
}

func readPreviewFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	return data, nil
}

func marshalPreviewJSON(value any) ([]byte, error) {
	return json.MarshalIndent(value, "", "  ")
}

func normalizePreviewData(kind previewFileKind, data []byte) string {
	switch kind {
	case previewJSON:
		var value any
		if err := json.Unmarshal(data, &value); err == nil {
			redacted := redactPreviewValue("", value)
			if normalized, err := json.MarshalIndent(redacted, "", "  "); err == nil {
				return string(normalized)
			}
		}
	case previewEnv:
		return redactPreviewEnv(string(data))
	}
	return redactPreviewText(string(data))
}

func redactPreviewValue(key string, value any) any {
	if isSensitivePreviewKey(key) {
		return "[REDACTED]"
	}

	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			out[childKey] = redactPreviewValue(childKey, childValue)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, childValue := range typed {
			out[index] = redactPreviewValue(key, childValue)
		}
		return out
	case string:
		if strings.Contains(strings.ToLower(key), "url") {
			return redactPreviewURL(typed)
		}
	}
	return value
}

func redactPreviewEnv(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for index, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		trimmedKey := strings.TrimSpace(key)
		if isSensitivePreviewKey(trimmedKey) {
			lines[index] = key + "=[REDACTED]"
			continue
		}
		if strings.Contains(strings.ToLower(trimmedKey), "url") {
			lines[index] = key + "=" + redactPreviewURL(strings.TrimSpace(value))
		}
	}
	return strings.Join(lines, "\n")
}

func redactPreviewText(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for index, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if ok && isSensitivePreviewKey(strings.TrimSpace(key)) {
			lines[index] = key + " = [REDACTED]"
		}
	}
	return strings.Join(lines, "\n")
}

func isSensitivePreviewKey(key string) bool {
	compact := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
	for _, marker := range []string{"apikey", "authtoken", "accesstoken", "refreshtoken", "clientsecret", "password", "secret"} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

func redactPreviewURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.User != nil {
		parsed.User = url.User(parsed.User.Username())
	}
	query := parsed.Query()
	changed := false
	for key := range query {
		if isSensitivePreviewKey(key) || strings.Contains(strings.ToLower(key), "key") {
			query.Set(key, "[REDACTED]")
			changed = true
		}
	}
	if changed {
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

func diffPreviewLines(oldText, newText string) ([]string, int, int) {
	oldLines := splitPreviewLines(oldText)
	newLines := splitPreviewLines(newText)
	if oldText == newText {
		return []string{"  (no changes)"}, 0, 0
	}

	// Avoid an excessive allocation for very large configuration files. A
	// complete replacement still gives the user an honest preview.
	if len(oldLines)*len(newLines) > 4_000_000 {
		lines := make([]string, 0, len(oldLines)+len(newLines))
		for _, line := range oldLines {
			lines = append(lines, "- "+line)
		}
		for _, line := range newLines {
			lines = append(lines, "+ "+line)
		}
		return lines, len(newLines), len(oldLines)
	}

	dp := make([][]int, len(oldLines)+1)
	for index := range dp {
		dp[index] = make([]int, len(newLines)+1)
	}
	for oldIndex := len(oldLines) - 1; oldIndex >= 0; oldIndex-- {
		for newIndex := len(newLines) - 1; newIndex >= 0; newIndex-- {
			if oldLines[oldIndex] == newLines[newIndex] {
				dp[oldIndex][newIndex] = dp[oldIndex+1][newIndex+1] + 1
			} else if dp[oldIndex+1][newIndex] >= dp[oldIndex][newIndex+1] {
				dp[oldIndex][newIndex] = dp[oldIndex+1][newIndex]
			} else {
				dp[oldIndex][newIndex] = dp[oldIndex][newIndex+1]
			}
		}
	}

	lines := make([]string, 0, len(oldLines)+len(newLines))
	added, removed := 0, 0
	oldIndex, newIndex := 0, 0
	for oldIndex < len(oldLines) || newIndex < len(newLines) {
		if oldIndex < len(oldLines) && newIndex < len(newLines) && oldLines[oldIndex] == newLines[newIndex] {
			lines = append(lines, "  "+oldLines[oldIndex])
			oldIndex++
			newIndex++
			continue
		}
		if oldIndex < len(oldLines) && (newIndex == len(newLines) || dp[oldIndex+1][newIndex] >= dp[oldIndex][newIndex+1]) {
			lines = append(lines, "- "+oldLines[oldIndex])
			oldIndex++
			removed++
			continue
		}
		lines = append(lines, "+ "+newLines[newIndex])
		newIndex++
		added++
	}
	return lines, added, removed
}

func splitPreviewLines(content string) []string {
	content = strings.TrimSuffix(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}
