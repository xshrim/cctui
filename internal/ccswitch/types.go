package ccswitch

import (
	"encoding/json"
	"strings"
)

type AppType string

const (
	AppClaude   AppType = "claude"
	AppCodex    AppType = "codex"
	AppGemini   AppType = "gemini"
	AppOpencode AppType = "opencode"
)

var AllAppTypes = []AppType{AppClaude, AppCodex, AppGemini, AppOpencode}

func (a AppType) String() string {
	return string(a)
}

func (a AppType) DisplayName() string {
	switch a {
	case AppClaude:
		return "Claude"
	case AppCodex:
		return "Codex"
	case AppGemini:
		return "Gemini"
	case AppOpencode:
		return "Opencode"
	default:
		return strings.Title(string(a))
	}
}

// IsIncremental 返回该应用类型的配置是否为增量模式（多 provider 共存于同一配置文件）
func (a AppType) IsIncremental() bool {
	return a == AppOpencode
}

type Provider struct {
	ID              string
	Name            string
	SettingsConfig  map[string]any
	WebsiteURL      *string
	Category        *string
	CreatedAt       *int64
	SortIndex       *int64
	Notes           *string
	Meta            map[string]any
	Icon            *string
	IconColor       *string
	InFailoverQueue bool
}

func (p Provider) Clone() Provider {
	clone := p
	clone.SettingsConfig = CloneMap(p.SettingsConfig)
	clone.Meta = CloneMap(p.Meta)
	clone.WebsiteURL = cloneStringPtr(p.WebsiteURL)
	clone.Category = cloneStringPtr(p.Category)
	clone.CreatedAt = cloneInt64Ptr(p.CreatedAt)
	clone.SortIndex = cloneInt64Ptr(p.SortIndex)
	clone.Notes = cloneStringPtr(p.Notes)
	clone.Icon = cloneStringPtr(p.Icon)
	clone.IconColor = cloneStringPtr(p.IconColor)
	return clone
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

type ProviderInput struct {
	Name              string
	BaseURL           string
	APIKey            string
	ClaudeAPIKeyField string
	GeminiAuthMode    string
	Model             string
	ReasoningEffort   string
	Website           string
	Notes             string
}

type Snapshot struct {
	Providers map[AppType][]Provider
	Current   map[AppType]string
}

func CloneMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}

	buf, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}

	var out map[string]any
	if err := json.Unmarshal(buf, &out); err != nil {
		return map[string]any{}
	}

	return out
}
