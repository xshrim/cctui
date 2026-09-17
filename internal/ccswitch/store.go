package ccswitch

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"

	toml "github.com/pelletier/go-toml/v2"
	_ "modernc.org/sqlite"
)

var (
	baseURLRe         = regexp.MustCompile(`(?m)base_url\s*=\s*["']([^"']+)["']`)
	modelRe           = regexp.MustCompile(`(?m)^model\s*=\s*["']([^"']+)["']`)
	reasoningEffortRe = regexp.MustCompile(`(?m)^model_reasoning_effort\s*=\s*["']([^"']+)["']`)
)

type Store struct {
	db       *sql.DB
	settings *settingsStore
}

type settingsStore struct {
	path string
	raw  map[string]any
}

func OpenStore() (*Store, error) {
	appDir := filepath.Join(homeDir(), ".cc-switch")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建配置目录失败: %w", err)
	}

	settings, err := loadSettingsStore(filepath.Join(appDir, "settings.json"))
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", filepath.Join(appDir, "cc-switch.db"))
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	store := &Store{db: db, settings: settings}
	if err := store.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// CodexConfigDir returns the directory used by Codex live configuration and
// session data. It honours the optional codexConfigDir override in settings.
func (s *Store) CodexConfigDir() string {
	return s.configDirFor(AppCodex)
}

func (s *Store) Bootstrap() ([]string, error) {
	var warnings []string

	for _, app := range AllAppTypes {
		providers, err := s.ListProviders(app)
		if err != nil {
			return warnings, err
		}
		if len(providers) > 0 {
			continue
		}

		imported, err := s.importCurrentLive(app)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s 导入失败: %v", app.DisplayName(), err))
			continue
		}
		if imported {
			warnings = append(warnings, fmt.Sprintf("已导入 %s 当前 live 配置", app.DisplayName()))
		}
	}

	return warnings, nil
}

func (s *Store) Snapshot() (*Snapshot, error) {
	out := &Snapshot{
		Providers: make(map[AppType][]Provider, len(AllAppTypes)),
		Current:   make(map[AppType]string, len(AllAppTypes)),
	}

	for _, app := range AllAppTypes {
		providers, err := s.ListProviders(app)
		if err != nil {
			return nil, err
		}
		out.Providers[app] = providers

		current, err := s.GetEffectiveCurrentProvider(app)
		if err != nil {
			return nil, err
		}
		out.Current[app] = current
	}

	return out, nil
}

func (s *Store) ListProviders(app AppType) ([]Provider, error) {
	rows, err := s.db.Query(`
		SELECT id, name, settings_config, website_url, category, created_at, sort_index, notes, icon, icon_color, meta, in_failover_queue
		FROM providers
		WHERE app_type = ?
		ORDER BY COALESCE(sort_index, 999999), created_at ASC, id ASC
	`, app.String())
	if err != nil {
		return nil, fmt.Errorf("读取 %s 供应商失败: %w", app.DisplayName(), err)
	}
	defer rows.Close()

	var providers []Provider
	for rows.Next() {
		var (
			id, name               string
			settingsJSON, metaJSON string
			websiteURL, category   sql.NullString
			createdAt, sortIndex   sql.NullInt64
			notes, icon, iconColor sql.NullString
			inFailoverQueue        bool
			settingsConfig         map[string]any
			meta                   map[string]any
		)

		if err := rows.Scan(
			&id,
			&name,
			&settingsJSON,
			&websiteURL,
			&category,
			&createdAt,
			&sortIndex,
			&notes,
			&icon,
			&iconColor,
			&metaJSON,
			&inFailoverQueue,
		); err != nil {
			return nil, fmt.Errorf("扫描 %s 供应商失败: %w", app.DisplayName(), err)
		}

		if err := json.Unmarshal([]byte(settingsJSON), &settingsConfig); err != nil {
			settingsConfig = map[string]any{}
		}
		if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
			meta = map[string]any{}
		}

		provider := Provider{
			ID:              id,
			Name:            name,
			SettingsConfig:  settingsConfig,
			WebsiteURL:      nullStringPtr(websiteURL),
			Category:        nullStringPtr(category),
			CreatedAt:       nullInt64Ptr(createdAt),
			SortIndex:       nullInt64Ptr(sortIndex),
			Notes:           nullStringPtr(notes),
			Meta:            meta,
			Icon:            nullStringPtr(icon),
			IconColor:       nullStringPtr(iconColor),
			InFailoverQueue: inFailoverQueue,
		}
		providers = append(providers, provider)
	}

	return providers, rows.Err()
}

func (s *Store) GetProvider(app AppType, id string) (*Provider, error) {
	var (
		name, settingsJSON, metaJSON string
		websiteURL, category         sql.NullString
		createdAt, sortIndex         sql.NullInt64
		notes, icon, iconColor       sql.NullString
		inFailoverQueue              bool
		settingsConfig               map[string]any
		meta                         map[string]any
	)

	err := s.db.QueryRow(`
		SELECT name, settings_config, website_url, category, created_at, sort_index, notes, icon, icon_color, meta, in_failover_queue
		FROM providers
		WHERE id = ? AND app_type = ?
	`, id, app.String()).Scan(
		&name,
		&settingsJSON,
		&websiteURL,
		&category,
		&createdAt,
		&sortIndex,
		&notes,
		&icon,
		&iconColor,
		&metaJSON,
		&inFailoverQueue,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取供应商失败: %w", err)
	}

	if err := json.Unmarshal([]byte(settingsJSON), &settingsConfig); err != nil {
		settingsConfig = map[string]any{}
	}
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		meta = map[string]any{}
	}

	return &Provider{
		ID:              id,
		Name:            name,
		SettingsConfig:  settingsConfig,
		WebsiteURL:      nullStringPtr(websiteURL),
		Category:        nullStringPtr(category),
		CreatedAt:       nullInt64Ptr(createdAt),
		SortIndex:       nullInt64Ptr(sortIndex),
		Notes:           nullStringPtr(notes),
		Meta:            meta,
		Icon:            nullStringPtr(icon),
		IconColor:       nullStringPtr(iconColor),
		InFailoverQueue: inFailoverQueue,
	}, nil
}

func (s *Store) AddProvider(app AppType, input ProviderInput) (*Provider, bool, error) {
	providers, err := s.ListProviders(app)
	if err != nil {
		return nil, false, err
	}

	id := uniqueProviderID(input.Name, providers, app)
	provider, err := s.buildProvider(app, nil, input, id)
	if err != nil {
		return nil, false, err
	}

	now := time.Now().UnixMilli()
	sortIndex := nextSortIndex(providers)
	provider.CreatedAt = &now
	provider.SortIndex = &sortIndex

	if err := s.saveProviderRow(app, provider); err != nil {
		return nil, false, err
	}

	autoSwitched := false
	current, err := s.GetEffectiveCurrentProvider(app)
	if err != nil {
		return nil, false, err
	}

	// 增量模式：所有 provider 共存于同一配置文件，每次添加都要同步
	if app.IsIncremental() {
		if err := s.writeLiveSettings(app, provider); err != nil {
			return nil, false, err
		}
	}

	if current == "" {
		if !app.IsIncremental() {
			if err := s.writeLiveSettings(app, provider); err != nil {
				return nil, false, err
			}
		}
		if err := s.setCurrentProvider(app, provider.ID); err != nil {
			return nil, false, err
		}
		autoSwitched = true
	}

	return &provider, autoSwitched, nil
}

func (s *Store) UpdateProvider(app AppType, existing Provider, input ProviderInput) (*Provider, error) {
	provider, err := s.buildProvider(app, &existing, input, existing.ID)
	if err != nil {
		return nil, err
	}
	provider.CreatedAt = existing.CreatedAt
	provider.SortIndex = existing.SortIndex
	provider.InFailoverQueue = existing.InFailoverQueue
	provider.Icon = existing.Icon
	provider.IconColor = existing.IconColor
	provider.Category = existing.Category

	if err := s.saveProviderRow(app, provider); err != nil {
		return nil, err
	}

	// 增量模式：所有 provider 共存于同一配置文件，每次更新都要同步
	if app.IsIncremental() {
		if err := s.writeLiveSettings(app, provider); err != nil {
			return nil, err
		}
	} else {
		current, err := s.GetEffectiveCurrentProvider(app)
		if err != nil {
			return nil, err
		}
		if current == existing.ID {
			if err := s.writeLiveSettings(app, provider); err != nil {
				return nil, err
			}
		}
	}

	return &provider, nil
}

func (s *Store) DeleteProvider(app AppType, id string) error {
	providers, err := s.ListProviders(app)
	if err != nil {
		return err
	}

	current, err := s.GetEffectiveCurrentProvider(app)
	if err != nil {
		return err
	}
	if current == id {
		if len(providers) > 1 {
			return fmt.Errorf("不能删除当前正在使用的供应商，请先切换到其他供应商")
		}
	}

	if _, err := s.db.Exec(`DELETE FROM providers WHERE id = ? AND app_type = ?`, id, app.String()); err != nil {
		return fmt.Errorf("删除供应商失败: %w", err)
	}

	// 增量模式：从配置文件中移除对应的 provider 条目
	if app.IsIncremental() {
		for _, p := range providers {
			if p.ID == id {
				key := opencodeProviderKey(p.SettingsConfig, id)
				s.removeOpencodeProviderEntry(key)
				break
			}
		}
	}

	if current == id {
		s.settings.setString(currentProviderKey(app), "")
		if err := s.settings.save(); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) SwitchProvider(app AppType, id string) error {
	target, err := s.GetProvider(app, id)
	if err != nil {
		return err
	}
	if target == nil {
		return fmt.Errorf("供应商不存在")
	}

	current, err := s.GetEffectiveCurrentProvider(app)
	if err != nil {
		return err
	}
	if current == id {
		return nil
	}

	if current != "" {
		if liveSettings, err := s.readLiveSettings(app); err == nil {
			if existing, err := s.GetProvider(app, current); err == nil && existing != nil {
				existing.SettingsConfig = liveSettings
				_ = s.saveProviderRow(app, *existing)
			}
		}
	}

	if err := s.writeLiveSettings(app, *target); err != nil {
		return err
	}
	if err := s.setCurrentProvider(app, id); err != nil {
		return err
	}

	return nil
}

func (s *Store) GetEffectiveCurrentProvider(app AppType) (string, error) {
	localKey := currentProviderKey(app)
	if local := s.settings.getString(localKey); local != "" {
		exists, err := s.providerExists(app, local)
		if err != nil {
			return "", err
		}
		if exists {
			return local, nil
		}
		s.settings.setString(localKey, "")
		if err := s.settings.save(); err != nil {
			return "", err
		}
	}

	var current sql.NullString
	err := s.db.QueryRow(`
		SELECT id FROM providers WHERE app_type = ? AND is_current = 1 LIMIT 1
	`, app.String()).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取当前供应商失败: %w", err)
	}
	if current.Valid {
		return current.String, nil
	}
	return "", nil
}

func (s *Store) ExtractInput(app AppType, provider Provider) ProviderInput {
	switch app {
	case AppClaude:
		env := getOrCreateMap(provider.SettingsConfig, "env")
		apiKey := stringValue(env["ANTHROPIC_AUTH_TOKEN"])
		if apiKey == "" {
			apiKey = stringValue(env["ANTHROPIC_API_KEY"])
		}

		return ProviderInput{
			Name:    provider.Name,
			BaseURL: stringValue(env["ANTHROPIC_BASE_URL"]),
			APIKey:  apiKey,
			Model: firstNonEmpty(
				stringValue(env["ANTHROPIC_MODEL"]),
				stringValue(env["ANTHROPIC_DEFAULT_SONNET_MODEL"]),
				stringValue(env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]),
				stringValue(env["ANTHROPIC_DEFAULT_OPUS_MODEL"]),
			),
			Website: deref(provider.WebsiteURL),
			Notes:   deref(provider.Notes),
		}
	case AppCodex:
		auth := getOrCreateMap(provider.SettingsConfig, "auth")
		configText := stringValue(provider.SettingsConfig["config"])
		return ProviderInput{
			Name:            provider.Name,
			BaseURL:         extractFirstMatch(baseURLRe, configText),
			APIKey:          stringValue(auth["OPENAI_API_KEY"]),
			Model:           extractFirstMatch(modelRe, configText),
			ReasoningEffort: extractFirstMatch(reasoningEffortRe, configText),
			Website:         deref(provider.WebsiteURL),
			Notes:           deref(provider.Notes),
		}
	case AppGemini:
		env := getOrCreateMap(provider.SettingsConfig, "env")
		return ProviderInput{
			Name:    provider.Name,
			BaseURL: stringValue(env["GOOGLE_GEMINI_BASE_URL"]),
			APIKey:  stringValue(env["GEMINI_API_KEY"]),
			Model:   stringValue(env["GEMINI_MODEL"]),
			Website: deref(provider.WebsiteURL),
			Notes:   deref(provider.Notes),
		}
	case AppOpencode:
		options, modelsMap := opencodeProviderConfig(provider.SettingsConfig, provider.ID)
		firstModelName := ""
		for name := range modelsMap {
			firstModelName = name
			break
		}
		return ProviderInput{
			Name:    provider.Name,
			BaseURL: stringValue(options["baseURL"]),
			APIKey:  stringValue(options["apiKey"]),
			Model:   firstModelName,
			Website: deref(provider.WebsiteURL),
			Notes:   deref(provider.Notes),
		}
	default:
		return ProviderInput{Name: provider.Name}
	}
}

func (s *Store) EndpointSummary(app AppType, provider Provider) string {
	switch app {
	case AppClaude:
		env := getOrCreateMap(provider.SettingsConfig, "env")
		baseURL := strings.TrimSpace(stringValue(env["ANTHROPIC_BASE_URL"]))
		if baseURL == "" {
			return "官方登录"
		}
		return summarizeURL(baseURL)
	case AppCodex:
		configText := stringValue(provider.SettingsConfig["config"])
		baseURL := strings.TrimSpace(extractFirstMatch(baseURLRe, configText))
		if baseURL == "" {
			return "官方登录"
		}
		return summarizeURL(baseURL)
	case AppGemini:
		env := getOrCreateMap(provider.SettingsConfig, "env")
		baseURL := strings.TrimSpace(stringValue(env["GOOGLE_GEMINI_BASE_URL"]))
		if baseURL == "" {
			return "Google OAuth"
		}
		return summarizeURL(baseURL)
	case AppOpencode:
		options, _ := opencodeProviderConfig(provider.SettingsConfig, provider.ID)
		if baseURL := strings.TrimSpace(stringValue(options["baseURL"])); baseURL != "" {
			return summarizeURL(baseURL)
		}
		return "-"
	default:
		return "-"
	}
}

func (s *Store) ensureSchema() error {
	stmts := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS providers (
			id TEXT NOT NULL,
			app_type TEXT NOT NULL,
			name TEXT NOT NULL,
			settings_config TEXT NOT NULL,
			website_url TEXT,
			category TEXT,
			created_at INTEGER,
			sort_index INTEGER,
			notes TEXT,
			icon TEXT,
			icon_color TEXT,
			meta TEXT NOT NULL DEFAULT '{}',
			is_current BOOLEAN NOT NULL DEFAULT 0,
			in_failover_queue BOOLEAN NOT NULL DEFAULT 0,
			PRIMARY KEY (id, app_type)
		)`,
		`CREATE TABLE IF NOT EXISTS provider_endpoints (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_id TEXT NOT NULL,
			app_type TEXT NOT NULL,
			url TEXT NOT NULL,
			added_at INTEGER,
			FOREIGN KEY (provider_id, app_type) REFERENCES providers(id, app_type) ON DELETE CASCADE
		)`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("初始化数据库失败: %w", err)
		}
	}

	return nil
}

func (s *Store) importCurrentLive(app AppType) (bool, error) {
	live, err := s.readLiveSettings(app)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}

	id := uniqueProviderID("imported-"+app.DisplayName(), nil, app)
	now := time.Now().UnixMilli()
	sortIndex := int64(0)
	provider := Provider{
		ID:             id,
		Name:           "Imported " + app.DisplayName(),
		SettingsConfig: live,
		CreatedAt:      &now,
		SortIndex:      &sortIndex,
		Meta:           map[string]any{},
	}

	if err := s.saveProviderRow(app, provider); err != nil {
		return false, err
	}
	if err := s.setCurrentProvider(app, provider.ID); err != nil {
		return false, err
	}

	return true, nil
}

func (s *Store) saveProviderRow(app AppType, provider Provider) error {
	if strings.TrimSpace(provider.Name) == "" {
		return fmt.Errorf("供应商名称不能为空")
	}
	if provider.SettingsConfig == nil {
		provider.SettingsConfig = map[string]any{}
	}
	if provider.Meta == nil {
		provider.Meta = map[string]any{}
	}

	settingsJSON, err := json.Marshal(provider.SettingsConfig)
	if err != nil {
		return fmt.Errorf("序列化 settingsConfig 失败: %w", err)
	}
	metaJSON, err := json.Marshal(provider.Meta)
	if err != nil {
		return fmt.Errorf("序列化 meta 失败: %w", err)
	}

	isCurrent := false
	var existingCurrent, existingInFailover bool
	err = s.db.QueryRow(`
		SELECT is_current, in_failover_queue
		FROM providers
		WHERE id = ? AND app_type = ?
	`, provider.ID, app.String()).Scan(&existingCurrent, &existingInFailover)
	if err == nil {
		isCurrent = existingCurrent
		provider.InFailoverQueue = existingInFailover
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("读取现有供应商状态失败: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO providers (
			id, app_type, name, settings_config, website_url, category, created_at, sort_index, notes, icon, icon_color, meta, is_current, in_failover_queue
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id, app_type) DO UPDATE SET
			name = excluded.name,
			settings_config = excluded.settings_config,
			website_url = excluded.website_url,
			category = excluded.category,
			created_at = excluded.created_at,
			sort_index = excluded.sort_index,
			notes = excluded.notes,
			icon = excluded.icon,
			icon_color = excluded.icon_color,
			meta = excluded.meta,
			is_current = ?,
			in_failover_queue = ?
	`,
		provider.ID,
		app.String(),
		provider.Name,
		string(settingsJSON),
		nullableString(provider.WebsiteURL),
		nullableString(provider.Category),
		nullableInt64(provider.CreatedAt),
		nullableInt64(provider.SortIndex),
		nullableString(provider.Notes),
		nullableString(provider.Icon),
		nullableString(provider.IconColor),
		string(metaJSON),
		isCurrent,
		provider.InFailoverQueue,
		isCurrent,
		provider.InFailoverQueue,
	)
	if err != nil {
		return fmt.Errorf("保存供应商失败: %w", err)
	}

	return nil
}

func (s *Store) setCurrentProvider(app AppType, id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`UPDATE providers SET is_current = 0 WHERE app_type = ?`, app.String()); err != nil {
		return fmt.Errorf("重置当前供应商失败: %w", err)
	}
	if _, err := tx.Exec(`UPDATE providers SET is_current = 1 WHERE id = ? AND app_type = ?`, id, app.String()); err != nil {
		return fmt.Errorf("设置当前供应商失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交当前供应商失败: %w", err)
	}

	s.settings.setString(currentProviderKey(app), id)
	if err := s.settings.save(); err != nil {
		return err
	}

	return nil
}

func (s *Store) providerExists(app AppType, id string) (bool, error) {
	var exists int
	if err := s.db.QueryRow(`
		SELECT 1 FROM providers WHERE id = ? AND app_type = ? LIMIT 1
	`, id, app.String()).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("检查供应商是否存在失败: %w", err)
	}
	return true, nil
}

func (s *Store) buildProvider(app AppType, existing *Provider, input ProviderInput, id string) (Provider, error) {
	var provider Provider
	if existing != nil {
		provider = existing.Clone()
	} else {
		provider = Provider{
			ID:             id,
			SettingsConfig: map[string]any{},
			Meta:           map[string]any{},
		}
	}

	provider.ID = id
	provider.Name = strings.TrimSpace(input.Name)
	provider.WebsiteURL = stringPtrOrNil(input.Website)
	provider.Notes = stringPtrOrNil(input.Notes)

	switch app {
	case AppClaude:
		settings := CloneMap(provider.SettingsConfig)
		env := getOrCreateMap(settings, "env")
		keyField := "ANTHROPIC_AUTH_TOKEN"
		if existing != nil {
			if stringValue(existing.Meta["apiKeyField"]) == "ANTHROPIC_API_KEY" {
				keyField = "ANTHROPIC_API_KEY"
			}
			existingEnv := getOrCreateMap(existing.SettingsConfig, "env")
			if stringValue(existingEnv["ANTHROPIC_API_KEY"]) != "" && stringValue(existingEnv["ANTHROPIC_AUTH_TOKEN"]) == "" {
				keyField = "ANTHROPIC_API_KEY"
			}
		}

		delete(env, "ANTHROPIC_AUTH_TOKEN")
		delete(env, "ANTHROPIC_API_KEY")
		if value := strings.TrimSpace(input.APIKey); value != "" {
			env[keyField] = value
		}
		patchStringField(env, "ANTHROPIC_BASE_URL", input.BaseURL)

		model := strings.TrimSpace(input.Model)
		if model == "" {
			delete(env, "ANTHROPIC_MODEL")
			delete(env, "ANTHROPIC_DEFAULT_HAIKU_MODEL")
			delete(env, "ANTHROPIC_DEFAULT_SONNET_MODEL")
			delete(env, "ANTHROPIC_DEFAULT_OPUS_MODEL")
		} else {
			env["ANTHROPIC_MODEL"] = model
			env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = model
			env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = model
			env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = model
		}

		settings["env"] = env
		provider.SettingsConfig = settings

	case AppCodex:
		settings := CloneMap(provider.SettingsConfig)
		auth := getOrCreateMap(settings, "auth")
		patchStringField(auth, "OPENAI_API_KEY", input.APIKey)
		settings["auth"] = auth

		configText, err := patchCodexConfig(stringValue(settings["config"]), input)
		if err != nil {
			return Provider{}, err
		}
		settings["config"] = configText
		provider.SettingsConfig = settings

	case AppGemini:
		settings := CloneMap(provider.SettingsConfig)
		env := getOrCreateMap(settings, "env")
		patchStringField(env, "GOOGLE_GEMINI_BASE_URL", input.BaseURL)
		patchStringField(env, "GEMINI_API_KEY", input.APIKey)
		patchStringField(env, "GEMINI_MODEL", input.Model)
		settings["env"] = env
		provider.SettingsConfig = settings

	case AppOpencode:
		settings := CloneMap(provider.SettingsConfig)
		modelName := strings.TrimSpace(input.Model)
		if modelName == "" {
			modelName = "default"
		}

		modelsValue := map[string]any{
			modelName: map[string]any{"name": modelName},
		}
		optionsValue := map[string]any{"setCacheKey": true}
		// 保留已有 options
		existingOpts, _ := opencodeProviderConfig(settings, id)
		for k, v := range existingOpts {
			optionsValue[k] = v
		}
		patchStringField(optionsValue, "baseURL", input.BaseURL)
		patchStringField(optionsValue, "apiKey", input.APIKey)

		// 确定 provider key：优先复用现有 key，新建时用 slugify(name)
		provKey := opencodeProviderKey(settings, id)
		if provKey == id {
			if s := slugify(input.Name); s != "" {
				provKey = s
			}
		}

		provConfig := getOrCreateMap(settings, "provider")
		provConfig[provKey] = map[string]any{
			"models":  modelsValue,
			"options": optionsValue,
		}
		settings["provider"] = provConfig
		// 只保留嵌套结构，不存 top-level 冗余字段
		delete(settings, "models")
		delete(settings, "options")
		provider.SettingsConfig = settings
	}

	return provider, nil
}

func (s *Store) readLiveSettings(app AppType) (map[string]any, error) {
	switch app {
	case AppClaude:
		path := s.claudeSettingsPath()
		content, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, os.ErrNotExist
			}
			return nil, fmt.Errorf("读取 Claude live 配置失败: %w", err)
		}
		var settings map[string]any
		if err := json.Unmarshal(content, &settings); err != nil {
			return nil, fmt.Errorf("解析 Claude live 配置失败: %w", err)
		}
		return settings, nil

	case AppCodex:
		authExists := fileExists(s.codexAuthPath())
		configExists := fileExists(s.codexConfigPath())
		if !authExists && !configExists {
			return nil, os.ErrNotExist
		}

		auth := map[string]any{}
		if authExists {
			content, err := os.ReadFile(s.codexAuthPath())
			if err != nil {
				return nil, fmt.Errorf("读取 Codex auth.json 失败: %w", err)
			}
			if len(strings.TrimSpace(string(content))) > 0 {
				if err := json.Unmarshal(content, &auth); err != nil {
					return nil, fmt.Errorf("解析 Codex auth.json 失败: %w", err)
				}
			}
		}

		config := ""
		if configExists {
			content, err := os.ReadFile(s.codexConfigPath())
			if err != nil {
				return nil, fmt.Errorf("读取 Codex config.toml 失败: %w", err)
			}
			config = string(content)
		}

		return map[string]any{
			"auth":   auth,
			"config": config,
		}, nil

	case AppGemini:
		envExists := fileExists(s.geminiEnvPath())
		settingsExists := fileExists(s.geminiSettingsPath())
		if !envExists && !settingsExists {
			return nil, os.ErrNotExist
		}

		result := map[string]any{}
		envMap := map[string]string{}
		if envExists {
			content, err := os.ReadFile(s.geminiEnvPath())
			if err != nil {
				return nil, fmt.Errorf("读取 Gemini .env 失败: %w", err)
			}
			envMap = parseEnvFile(string(content))
		}

		envObject := make(map[string]any, len(envMap))
		for key, value := range envMap {
			envObject[key] = value
		}
		result["env"] = envObject

		if settingsExists {
			content, err := os.ReadFile(s.geminiSettingsPath())
			if err != nil {
				return nil, fmt.Errorf("读取 Gemini settings.json 失败: %w", err)
			}
			var settings map[string]any
			if err := json.Unmarshal(content, &settings); err != nil {
				return nil, fmt.Errorf("解析 Gemini settings.json 失败: %w", err)
			}
			result["config"] = settings
		}

		return result, nil

	case AppOpencode:
		path := s.opencodeConfigPath()
		content, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, os.ErrNotExist
			}
			return nil, fmt.Errorf("读取 Opencode 配置失败: %w", err)
		}
		var settings map[string]any
		if err := json.Unmarshal(content, &settings); err != nil {
			return nil, fmt.Errorf("解析 Opencode 配置失败: %w", err)
		}
		return settings, nil
	}

	return nil, fmt.Errorf("不支持的应用类型: %s", app)
}

func (s *Store) writeLiveSettings(app AppType, provider Provider) error {
	switch app {
	case AppClaude:
		return writeJSONAtomic(s.claudeSettingsPath(), provider.SettingsConfig)
	case AppCodex:
		auth := getOrCreateMap(provider.SettingsConfig, "auth")
		config := stringValue(provider.SettingsConfig["config"])
		return writeCodexLiveAtomic(s.codexAuthPath(), s.codexConfigPath(), auth, config)
	case AppGemini:
		return s.writeGeminiLive(provider)
	case AppOpencode:
		return s.writeOpencodeLive(provider)
	default:
		return fmt.Errorf("不支持的应用类型: %s", app)
	}
}

func (s *Store) writeGeminiLive(provider Provider) error {
	settings := CloneMap(provider.SettingsConfig)
	env := getOrCreateMap(settings, "env")
	envMap := map[string]string{}
	for key, value := range env {
		if text := strings.TrimSpace(stringValue(value)); text != "" {
			envMap[key] = text
		}
	}

	configDoc := map[string]any{}
	if fileExists(s.geminiSettingsPath()) {
		content, err := os.ReadFile(s.geminiSettingsPath())
		if err != nil {
			return fmt.Errorf("读取 Gemini settings.json 失败: %w", err)
		}
		if len(strings.TrimSpace(string(content))) > 0 {
			_ = json.Unmarshal(content, &configDoc)
		}
	}
	if rawConfig, ok := settings["config"]; ok {
		if rawConfig == nil {
			// 保持现有 settings.json
		} else if configMap, ok := rawConfig.(map[string]any); ok {
			mergeMaps(configDoc, configMap)
		} else {
			return fmt.Errorf("Gemini config 字段必须是对象或 null")
		}
	}

	selectedType := "gemini-api-key"
	if len(envMap) == 0 {
		selectedType = "oauth-personal"
	}
	setNestedMapValue(configDoc, []string{"security", "auth", "selectedType"}, selectedType)

	if err := writeTextAtomic(s.geminiEnvPath(), serializeEnvFile(envMap)); err != nil {
		return fmt.Errorf("写入 Gemini .env 失败: %w", err)
	}
	if err := writeJSONAtomic(s.geminiSettingsPath(), configDoc); err != nil {
		return fmt.Errorf("写入 Gemini settings.json 失败: %w", err)
	}

	return nil
}

// opencodeProviderKey 从 SettingsConfig 中解析 opencode.json 对应的 provider key。
// 优先用 providerID 精确匹配，否则取第一个 key，最后 fallback 到 providerID 本身。
func opencodeProviderKey(settings map[string]any, providerID string) string {
	provConfigs, ok := settings["provider"].(map[string]any)
	if !ok || len(provConfigs) == 0 {
		return providerID
	}
	if _, found := provConfigs[providerID]; found {
		return providerID
	}
	for k := range provConfigs {
		return k
	}
	return providerID
}

// opencodeProviderConfig 从 SettingsConfig 中提取指定 provider 的 options 和 models。
func opencodeProviderConfig(settings map[string]any, providerID string) (options, models map[string]any) {
	key := opencodeProviderKey(settings, providerID)
	if provConfigs, ok := settings["provider"].(map[string]any); ok {
		if cfg, ok := provConfigs[key].(map[string]any); ok {
			options, _ = cfg["options"].(map[string]any)
			models, _ = cfg["models"].(map[string]any)
		}
	}
	if options == nil {
		options = map[string]any{}
	}
	return
}

func (s *Store) writeOpencodeLive(provider Provider) error {
	settings := CloneMap(provider.SettingsConfig)
	path := s.opencodeConfigPath()

	// 读取现有文件，保留其他 provider 和 $schema
	doc := map[string]any{}
	if content, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(content, &doc)
	}
	if schema, ok := settings["$schema"]; ok {
		doc["$schema"] = schema
	}

	// 合并当前 provider 条目到文件
	docProviders := getOrCreateMap(doc, "provider")
	key := opencodeProviderKey(settings, provider.ID)
	if srcProviders, ok := settings["provider"].(map[string]any); ok {
		if entry, ok := srcProviders[key]; ok {
			docProviders[key] = entry
		}
	}
	doc["provider"] = docProviders

	return writeJSONAtomic(path, doc)
}

func (s *Store) removeOpencodeProviderEntry(providerID string) {
	path := s.opencodeConfigPath()
	content, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(content, &doc); err != nil {
		return
	}
	providers, ok := doc["provider"].(map[string]any)
	if !ok {
		return
	}
	delete(providers, providerID)
	doc["provider"] = providers
	_ = writeJSONAtomic(path, doc)
}

func (s *Store) claudeSettingsPath() string {
	dir := s.configDirFor(AppClaude)
	settingsPath := filepath.Join(dir, "settings.json")
	if fileExists(settingsPath) {
		return settingsPath
	}
	legacyPath := filepath.Join(dir, "claude.json")
	if fileExists(legacyPath) {
		return legacyPath
	}
	return settingsPath
}

func (s *Store) codexAuthPath() string {
	return filepath.Join(s.configDirFor(AppCodex), "auth.json")
}

func (s *Store) codexConfigPath() string {
	return filepath.Join(s.configDirFor(AppCodex), "config.toml")
}

func (s *Store) geminiEnvPath() string {
	return filepath.Join(s.configDirFor(AppGemini), ".env")
}

func (s *Store) geminiSettingsPath() string {
	return filepath.Join(s.configDirFor(AppGemini), "settings.json")
}

func (s *Store) opencodeConfigPath() string {
	return filepath.Join(s.configDirFor(AppOpencode), "opencode.json")
}

func (s *Store) configDirFor(app AppType) string {
	key := configDirKey(app)
	if custom := strings.TrimSpace(s.settings.getString(key)); custom != "" {
		return resolveOverridePath(custom)
	}

	switch app {
	case AppClaude:
		return filepath.Join(homeDir(), ".claude")
	case AppCodex:
		return filepath.Join(homeDir(), ".codex")
	case AppGemini:
		return filepath.Join(homeDir(), ".gemini")
	case AppOpencode:
		return filepath.Join(homeDir(), ".config", "opencode")
	default:
		return homeDir()
	}
}

func loadSettingsStore(path string) (*settingsStore, error) {
	store := &settingsStore{
		path: path,
		raw:  map[string]any{},
	}

	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("读取 settings.json 失败: %w", err)
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		return store, nil
	}
	if err := json.Unmarshal(content, &store.raw); err != nil {
		return nil, fmt.Errorf("解析 settings.json 失败: %w", err)
	}
	return store, nil
}

func (s *settingsStore) getString(key string) string {
	if s == nil {
		return ""
	}
	if value, ok := s.raw[key]; ok {
		return stringValue(value)
	}
	return ""
}

func (s *settingsStore) setString(key, value string) {
	if s.raw == nil {
		s.raw = map[string]any{}
	}
	if strings.TrimSpace(value) == "" {
		delete(s.raw, key)
		return
	}
	s.raw[key] = value
}

func (s *settingsStore) save() error {
	return writeJSONAtomic(s.path, s.raw)
}

func currentProviderKey(app AppType) string {
	switch app {
	case AppClaude:
		return "currentProviderClaude"
	case AppCodex:
		return "currentProviderCodex"
	case AppGemini:
		return "currentProviderGemini"
	default:
		return ""
	}
}

func configDirKey(app AppType) string {
	switch app {
	case AppClaude:
		return "claudeConfigDir"
	case AppCodex:
		return "codexConfigDir"
	case AppGemini:
		return "geminiConfigDir"
	case AppOpencode:
		return "opencodeConfigDir"
	default:
		return ""
	}
}

func patchCodexConfig(existing string, input ProviderInput) (string, error) {
	trimmed := strings.TrimSpace(existing)
	if trimmed == "" {
		if strings.TrimSpace(input.BaseURL) == "" && strings.TrimSpace(input.Model) == "" && strings.TrimSpace(input.ReasoningEffort) == "" {
			return "", nil
		}

		doc := map[string]any{}
		if strings.TrimSpace(input.Model) != "" {
			doc["model"] = strings.TrimSpace(input.Model)
		}
		if strings.TrimSpace(input.ReasoningEffort) != "" {
			doc["model_reasoning_effort"] = strings.TrimSpace(input.ReasoningEffort)
		}
		if strings.TrimSpace(input.BaseURL) != "" {
			doc["model_provider"] = "custom"
			doc["disable_response_storage"] = true
			doc["model_providers"] = map[string]any{
				"custom": map[string]any{
					"name":                 "custom",
					"base_url":             strings.TrimSpace(input.BaseURL),
					"wire_api":             "responses",
					"requires_openai_auth": true,
				},
			}
		}

		buf, err := toml.Marshal(doc)
		if err != nil {
			return "", fmt.Errorf("生成 Codex config.toml 失败: %w", err)
		}
		return strings.TrimSpace(string(buf)), nil
	}

	doc := map[string]any{}
	if err := toml.Unmarshal([]byte(existing), &doc); err != nil {
		return "", fmt.Errorf("解析 Codex config.toml 失败: %w", err)
	}

	patchGenericMapString(doc, "model", input.Model)
	patchGenericMapString(doc, "model_reasoning_effort", input.ReasoningEffort)

	if providerKey, ok := doc["model_provider"].(string); ok && strings.TrimSpace(providerKey) != "" {
		modelProviders, ok := doc["model_providers"].(map[string]any)
		if !ok || modelProviders == nil {
			modelProviders = map[string]any{}
			doc["model_providers"] = modelProviders
		}
		providerTable, ok := modelProviders[providerKey].(map[string]any)
		if !ok || providerTable == nil {
			providerTable = map[string]any{}
			modelProviders[providerKey] = providerTable
		}
		patchGenericMapString(providerTable, "base_url", input.BaseURL)
		if strings.TrimSpace(input.BaseURL) != "" {
			if _, ok := providerTable["name"]; !ok {
				providerTable["name"] = providerKey
			}
			if _, ok := providerTable["wire_api"]; !ok {
				providerTable["wire_api"] = "responses"
			}
			if _, ok := providerTable["requires_openai_auth"]; !ok {
				providerTable["requires_openai_auth"] = true
			}
		}
	} else {
		patchGenericMapString(doc, "base_url", input.BaseURL)
	}

	buf, err := toml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("写回 Codex config.toml 失败: %w", err)
	}

	return strings.TrimSpace(string(buf)), nil
}

func parseEnvFile(content string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		result[key] = value
	}
	return result
}

func serializeEnvFile(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", key, env[key]))
	}
	return strings.Join(lines, "\n")
}

func writeCodexLiveAtomic(authPath, configPath string, auth map[string]any, config string) error {
	oldAuth, _ := os.ReadFile(authPath)
	authExisted := fileExists(authPath)

	if err := writeJSONAtomic(authPath, auth); err != nil {
		return fmt.Errorf("写入 Codex auth.json 失败: %w", err)
	}
	if err := writeTextAtomic(configPath, config); err != nil {
		if authExisted {
			_ = writeBytesAtomic(authPath, oldAuth)
		} else {
			_ = os.Remove(authPath)
		}
		return fmt.Errorf("写入 Codex config.toml 失败: %w", err)
	}
	return nil
}

func writeJSONAtomic(path string, data any) error {
	buf, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return writeBytesAtomic(path, buf)
}

func writeTextAtomic(path, text string) error {
	return writeBytesAtomic(path, []byte(text))
}

func writeBytesAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}

	if runtime.GOOS == "windows" && fileExists(path) {
		_ = os.Remove(path)
	}
	return os.Rename(tmp, path)
}

func uniqueProviderID(name string, providers []Provider, app AppType) string {
	base := slugify(name)
	if base == "" {
		base = "provider"
	}

	if len(providers) == 0 {
		return base
	}

	used := map[string]struct{}{}
	for _, provider := range providers {
		used[provider.ID] = struct{}{}
	}
	if _, exists := used[base]; !exists {
		return base
	}

	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s-%d", base, index)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func slugify(input string) string {
	var builder strings.Builder
	lastDash := false

	for _, char := range strings.ToLower(strings.TrimSpace(input)) {
		switch {
		case unicode.IsLetter(char) || unicode.IsDigit(char):
			builder.WriteRune(char)
			lastDash = false
		case char == '-' || char == '_' || unicode.IsSpace(char):
			if !lastDash && builder.Len() > 0 {
				builder.WriteRune('-')
				lastDash = true
			}
		}
	}

	out := strings.Trim(builder.String(), "-")
	return out
}

func nextSortIndex(providers []Provider) int64 {
	var maxValue int64 = -1
	for _, provider := range providers {
		if provider.SortIndex != nil && *provider.SortIndex > maxValue {
			maxValue = *provider.SortIndex
		}
	}
	return maxValue + 1
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func patchStringField(target map[string]any, key, value string) {
	if target == nil {
		return
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		delete(target, key)
		return
	}
	target[key] = trimmed
}

func patchGenericMapString(target map[string]any, key, value string) {
	if target == nil {
		return
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		delete(target, key)
		return
	}
	target[key] = trimmed
}

func getOrCreateMap(target map[string]any, key string) map[string]any {
	if target == nil {
		return map[string]any{}
	}
	if existing, ok := target[key].(map[string]any); ok && existing != nil {
		return existing
	}
	newMap := map[string]any{}
	target[key] = newMap
	return newMap
}

func mergeMaps(target, source map[string]any) {
	for key, value := range source {
		sourceMap, sourceIsMap := value.(map[string]any)
		targetMap, targetIsMap := target[key].(map[string]any)
		if sourceIsMap && targetIsMap {
			mergeMaps(targetMap, sourceMap)
			continue
		}
		target[key] = value
	}
}

func setNestedMapValue(target map[string]any, path []string, value any) {
	current := target
	for _, key := range path[:len(path)-1] {
		next, ok := current[key].(map[string]any)
		if !ok || next == nil {
			next = map[string]any{}
			current[key] = next
		}
		current = next
	}
	current[path[len(path)-1]] = value
}

func extractFirstMatch(re *regexp.Regexp, input string) string {
	match := re.FindStringSubmatch(input)
	if len(match) > 1 {
		return match[1]
	}
	return ""
}

func summarizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "-"
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return truncate(raw, 28)
	}
	host := parsed.Host
	if host == "" {
		host = raw
	}
	if parsed.Path != "" && parsed.Path != "/" {
		host += parsed.Path
	}
	return truncate(host, 28)
}

func truncate(input string, limit int) string {
	runes := []rune(input)
	if len(runes) <= limit {
		return input
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func resolveOverridePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if strings.HasPrefix(trimmed, "~/") {
		return filepath.Join(homeDir(), strings.TrimPrefix(trimmed, "~/"))
	}
	return trimmed
}

func homeDir() string {
	if testHome := strings.TrimSpace(os.Getenv("CC_SWITCH_TEST_HOME")); testHome != "" {
		return testHome
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return home
	}
	return "."
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func stringValue(value any) string {
	switch cast := value.(type) {
	case string:
		return cast
	case fmt.Stringer:
		return cast.String()
	default:
		return ""
	}
}

func stringPtrOrNil(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	number := value.Int64
	return &number
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
