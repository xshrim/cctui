package ccswitch

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"

	toml "github.com/pelletier/go-toml/v2"
	_ "modernc.org/sqlite"
)

const (
	opencodeProviderKeyMeta = "opencodeProviderKey"
	geminiAuthModeAPIKey    = "gemini-api-key"
	geminiAuthModeOAuth     = "oauth-personal"
	geminiAuthModeVertex    = "vertex-ai"
)

type Store struct {
	db       *sql.DB
	settings *settingsStore
	liveHash map[AppType]string
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

	store := &Store{db: db, settings: settings, liveHash: map[AppType]string{}}
	if err := store.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.normalizeIncrementalState(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) normalizeIncrementalState() error {
	if _, err := s.db.Exec(`UPDATE providers SET is_current = 0 WHERE app_type = ?`, AppOpencode.String()); err != nil {
		return fmt.Errorf("清理 Opencode 当前状态失败: %w", err)
	}
	if s.settings != nil && s.settings.raw != nil {
		if _, ok := s.settings.raw[""]; ok {
			delete(s.settings.raw, "")
			if err := s.settings.save(); err != nil {
				return fmt.Errorf("清理 Opencode 当前配置失败: %w", err)
			}
		}
	}
	return nil
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

// ConfigDir returns the effective native configuration directory used by an
// application. Native environment variables take precedence over cctui's
// optional settings override because they control where the CLI actually
// reads its configuration.
func (s *Store) ConfigDir(app AppType) string {
	return s.configDirFor(app)
}

// OpenCodeConfigPath returns the effective OpenCode config file, including an
// OPENCODE_CONFIG file override when one is set.
func (s *Store) OpenCodeConfigPath() string {
	return s.opencodeConfigPath()
}

// ConfigPaths returns the live configuration files used by an application.
func (s *Store) ConfigPaths(app AppType) []string {
	switch app {
	case AppClaude:
		return []string{s.claudeSettingsPath()}
	case AppCodex:
		return []string{s.codexAuthPath(), s.codexConfigPath()}
	case AppGemini:
		return []string{s.geminiEnvPath(), s.geminiSettingsPath()}
	case AppOpencode:
		return []string{s.opencodeConfigPath()}
	default:
		return nil
	}
}

func (s *Store) Bootstrap() ([]string, error) {
	var warnings []string

	for _, app := range AllAppTypes {
		providers, err := s.ListProviders(app)
		if err != nil {
			return warnings, err
		}
		if len(providers) > 0 {
			_ = s.refreshLiveHash(app)
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
		_ = s.refreshLiveHash(app)
	}

	return warnings, nil
}

func (s *Store) Snapshot() (*Snapshot, error) {
	out := &Snapshot{
		Providers: make(map[AppType][]Provider, len(AllAppTypes)),
		Current:   make(map[AppType]string, len(AllAppTypes)),
	}

	for _, app := range AllAppTypes {
		if _, ok := s.liveHash[app]; !ok {
			_ = s.refreshLiveHash(app)
		}
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
	if app == AppOpencode {
		live, liveErr := s.readLiveSettings(app)
		if liveErr != nil && !errors.Is(liveErr, os.ErrNotExist) {
			return nil, false, liveErr
		}
		id = uniqueOpencodeProviderKey(input.Name, providers, live)
	}
	provider, err := s.buildProvider(app, nil, input, id)
	if err != nil {
		return nil, false, err
	}
	if app == AppOpencode {
		if provider.Meta == nil {
			provider.Meta = map[string]any{}
		}
		provider.Meta[opencodeProviderKeyMeta] = id
	}

	now := time.Now().UnixMilli()
	sortIndex := nextSortIndex(providers)
	provider.CreatedAt = &now
	provider.SortIndex = &sortIndex
	if app.IsIncremental() {
		if err := s.checkLiveHash(app); err != nil {
			return nil, false, err
		}
	}

	if err := s.saveProviderRow(app, provider); err != nil {
		return nil, false, err
	}

	autoSwitched := false
	if app.IsIncremental() {
		if err := s.writeLiveSettings(app, provider); err != nil {
			return nil, false, err
		}
		_ = s.refreshLiveHash(app)
		return &provider, false, nil
	}

	current, err := s.GetEffectiveCurrentProvider(app)
	if err != nil {
		return nil, false, err
	}

	if current == "" {
		if err := s.writeLiveSettings(app, provider); err != nil {
			return nil, false, err
		}
		if err := s.setCurrentProvider(app, provider.ID); err != nil {
			return nil, false, err
		}
		_ = s.refreshLiveHash(app)
		autoSwitched = true
	}

	return &provider, autoSwitched, nil
}

func (s *Store) UpdateProvider(app AppType, existing Provider, input ProviderInput) (*Provider, error) {
	provider, err := s.buildProvider(app, &existing, input, existing.ID)
	if err != nil {
		return nil, err
	}
	if app.IsIncremental() {
		if err := s.checkLiveHash(app); err != nil {
			return nil, err
		}
	} else if current, err := s.GetEffectiveCurrentProvider(app); err != nil {
		return nil, err
	} else if current == existing.ID {
		if err := s.checkLiveHash(app); err != nil {
			return nil, err
		}
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
		_ = s.refreshLiveHash(app)
	} else {
		current, err := s.GetEffectiveCurrentProvider(app)
		if err != nil {
			return nil, err
		}
		if current == existing.ID {
			if err := s.writeLiveSettings(app, provider); err != nil {
				return nil, err
			}
			_ = s.refreshLiveHash(app)
		}
	}

	return &provider, nil
}

func (s *Store) DeleteProvider(app AppType, id string) error {
	providers, err := s.ListProviders(app)
	if err != nil {
		return err
	}

	current := ""
	if !app.IsIncremental() {
		current, err = s.GetEffectiveCurrentProvider(app)
		if err != nil {
			return err
		}
		if current == id && len(providers) > 1 {
			return fmt.Errorf("不能删除当前正在使用的供应商，请先切换到其他供应商")
		}
	} else if err := s.checkLiveHash(app); err != nil {
		return err
	}
	opencodeKey := ""
	if app.IsIncremental() {
		for _, provider := range providers {
			if provider.ID == id {
				opencodeKey = opencodeProviderKeyForProvider(provider)
				break
			}
		}
		if opencodeKey == "" {
			return fmt.Errorf("供应商不存在或缺少 OpenCode provider key: %s", id)
		}
		if err := s.removeOpencodeProviderEntry(opencodeKey); err != nil {
			return err
		}
	}

	result, err := s.db.Exec(`DELETE FROM providers WHERE id = ? AND app_type = ?`, id, app.String())
	if err != nil {
		return fmt.Errorf("删除供应商失败: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("检查删除结果失败: %w", err)
	} else if affected == 0 {
		return fmt.Errorf("供应商不存在: %s", id)
	}

	if !app.IsIncremental() && current == id {
		s.settings.setString(currentProviderKey(app), "")
		if err := s.settings.save(); err != nil {
			return err
		}
	}
	if app.IsIncremental() {
		_ = s.refreshLiveHash(app)
	}

	return nil
}

func (s *Store) SwitchProvider(app AppType, id string) error {
	if app.IsIncremental() {
		return fmt.Errorf("%s 为增量模式，不支持切换供应商", app.DisplayName())
	}
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
	if err := s.checkLiveHash(app); err != nil {
		return err
	}

	if current != "" {
		liveSettings, err := s.readLiveSettings(app)
		if err != nil {
			return fmt.Errorf("读取当前 %s live 配置失败，已取消切换: %w", app.DisplayName(), err)
		}
		existing, err := s.GetProvider(app, current)
		if err != nil {
			return err
		}
		if existing == nil {
			return fmt.Errorf("当前供应商不存在: %s", current)
		}
		existing.SettingsConfig = liveSettings
		if err := s.saveProviderRow(app, *existing); err != nil {
			return err
		}
	}

	if err := s.writeLiveSettings(app, *target); err != nil {
		return err
	}
	if err := s.setCurrentProvider(app, id); err != nil {
		return err
	}
	_ = s.refreshLiveHash(app)

	return nil
}

func (s *Store) GetEffectiveCurrentProvider(app AppType) (string, error) {
	if app.IsIncremental() {
		return "", nil
	}

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
			Name:              provider.Name,
			BaseURL:           stringValue(env["ANTHROPIC_BASE_URL"]),
			APIKey:            apiKey,
			ClaudeAPIKeyField: stringValue(provider.Meta["apiKeyField"]),
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
		baseURL, model, reasoningEffort, _ := parseCodexConfigFields(configText)
		return ProviderInput{
			Name:            provider.Name,
			BaseURL:         baseURL,
			APIKey:          stringValue(auth["OPENAI_API_KEY"]),
			Model:           model,
			ReasoningEffort: reasoningEffort,
			Website:         deref(provider.WebsiteURL),
			Notes:           deref(provider.Notes),
		}
	case AppGemini:
		env := getOrCreateMap(provider.SettingsConfig, "env")
		return ProviderInput{
			Name:           provider.Name,
			BaseURL:        stringValue(env["GOOGLE_GEMINI_BASE_URL"]),
			APIKey:         stringValue(env["GEMINI_API_KEY"]),
			GeminiAuthMode: stringValue(provider.Meta["geminiAuthMode"]),
			Model:          stringValue(env["GEMINI_MODEL"]),
			Website:        deref(provider.WebsiteURL),
			Notes:          deref(provider.Notes),
		}
	case AppOpencode:
		options, modelsMap := opencodeProviderConfigForProvider(provider)
		firstModelName := firstMapKey(modelsMap)
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
		baseURL, _, _, _ := parseCodexConfigFields(configText)
		baseURL = strings.TrimSpace(baseURL)
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
		options, _ := opencodeProviderConfigForProvider(provider)
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
	if app == AppOpencode {
		return s.importOpencodeProviders(live)
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
	if app == AppClaude {
		provider.Meta["apiKeyField"] = detectClaudeAPIKeyField(live)
	}
	if app == AppGemini {
		provider.Meta["geminiAuthMode"] = detectGeminiAuthMode(live)
	}

	if err := s.saveProviderRow(app, provider); err != nil {
		return false, err
	}
	if err := s.setCurrentProvider(app, provider.ID); err != nil {
		return false, err
	}

	return true, nil
}

func (s *Store) importOpencodeProviders(live map[string]any) (bool, error) {
	providersMap, ok := live["provider"].(map[string]any)
	if !ok || len(providersMap) == 0 {
		return false, nil
	}

	keys := make([]string, 0, len(providersMap))
	for key := range providersMap {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	now := time.Now().UnixMilli()
	for index, key := range keys {
		name := key
		if entry, ok := providersMap[key].(map[string]any); ok {
			if configuredName := stringValue(entry["name"]); configuredName != "" {
				name = configuredName
			}
		}
		provider := Provider{
			ID:             key,
			Name:           name,
			SettingsConfig: CloneMap(live),
			CreatedAt:      &now,
			SortIndex:      int64Ptr(int64(index)),
			Meta:           map[string]any{opencodeProviderKeyMeta: key},
		}
		if err := s.saveProviderRow(AppOpencode, provider); err != nil {
			return false, err
		}
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
	if err == nil && !app.IsIncremental() {
		isCurrent = existingCurrent
		provider.InFailoverQueue = existingInFailover
	} else if err == nil {
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
		keyField := strings.TrimSpace(input.ClaudeAPIKeyField)
		if keyField == "" && existing != nil {
			keyField = stringValue(existing.Meta["apiKeyField"])
		}
		if keyField != "ANTHROPIC_AUTH_TOKEN" && keyField != "ANTHROPIC_API_KEY" {
			keyField = "ANTHROPIC_API_KEY"
		}
		if existing != nil {
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
		if provider.Meta == nil {
			provider.Meta = map[string]any{}
		}
		provider.Meta["apiKeyField"] = keyField
		patchStringField(env, "ANTHROPIC_BASE_URL", input.BaseURL)

		model := strings.TrimSpace(input.Model)
		if existing == nil {
			patchStringField(env, "ANTHROPIC_MODEL", model)
		} else {
			existingEnv := getOrCreateMap(existing.SettingsConfig, "env")
			previousModel := firstNonEmpty(
				stringValue(existingEnv["ANTHROPIC_MODEL"]),
				stringValue(existingEnv["ANTHROPIC_DEFAULT_SONNET_MODEL"]),
				stringValue(existingEnv["ANTHROPIC_DEFAULT_HAIKU_MODEL"]),
				stringValue(existingEnv["ANTHROPIC_DEFAULT_OPUS_MODEL"]),
			)
			if model != previousModel {
				patchStringField(env, "ANTHROPIC_MODEL", model)
			}
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
		authMode := strings.TrimSpace(input.GeminiAuthMode)
		if authMode == "" {
			authMode = stringValue(provider.Meta["geminiAuthMode"])
		}
		if authMode == "" {
			authMode = detectGeminiAuthMode(settings)
		}
		if input.APIKey != "" {
			authMode = geminiAuthModeAPIKey
		} else if authMode != geminiAuthModeVertex {
			authMode = geminiAuthModeOAuth
		}
		if provider.Meta == nil {
			provider.Meta = map[string]any{}
		}
		provider.Meta["geminiAuthMode"] = authMode
		provider.SettingsConfig = settings

	case AppOpencode:
		settings := CloneMap(provider.SettingsConfig)
		provKey := id
		if existing != nil {
			provKey = opencodeProviderKeyForProvider(*existing)
			if provKey == "" {
				return Provider{}, fmt.Errorf("无法确定 OpenCode provider key: %s", id)
			}
		}

		provConfig := getOrCreateMap(settings, "provider")
		entry, _ := provConfig[provKey].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
		} else {
			entry = CloneMap(entry)
		}

		modelsValue, _ := entry["models"].(map[string]any)
		if modelsValue == nil {
			modelsValue = map[string]any{}
		} else {
			modelsValue = CloneMap(modelsValue)
		}
		modelName := strings.TrimSpace(input.Model)
		if modelName != "" {
			modelEntry, _ := modelsValue[modelName].(map[string]any)
			if modelEntry == nil {
				modelEntry = map[string]any{}
			} else {
				modelEntry = CloneMap(modelEntry)
			}
			modelEntry["name"] = modelName
			modelsValue[modelName] = modelEntry
		}
		if len(modelsValue) == 0 {
			modelName = "default"
			modelsValue[modelName] = map[string]any{"name": modelName}
		}

		optionsValue, _ := entry["options"].(map[string]any)
		if optionsValue == nil {
			optionsValue = map[string]any{}
		} else {
			optionsValue = CloneMap(optionsValue)
		}
		if _, ok := optionsValue["setCacheKey"]; !ok {
			optionsValue["setCacheKey"] = true
		}
		patchStringField(optionsValue, "baseURL", input.BaseURL)
		patchStringField(optionsValue, "apiKey", input.APIKey)

		entry["models"] = modelsValue
		entry["options"] = optionsValue
		provConfig[provKey] = entry
		settings["provider"] = provConfig
		if provider.Meta == nil {
			provider.Meta = map[string]any{}
		}
		provider.Meta[opencodeProviderKeyMeta] = provKey
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
		if err := unmarshalOpenCodeConfig(content, &settings); err != nil {
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
			if err := json.Unmarshal(content, &configDoc); err != nil {
				return fmt.Errorf("解析 Gemini settings.json 失败: %w", err)
			}
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

	selectedType := stringValue(provider.Meta["geminiAuthMode"])
	if selectedType != geminiAuthModeAPIKey && selectedType != geminiAuthModeVertex && selectedType != geminiAuthModeOAuth {
		selectedType = detectGeminiAuthMode(map[string]any{"env": env})
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
// 只有精确匹配或唯一 provider 时才返回结果，避免随机选择 map 中的 key。
func opencodeProviderKey(settings map[string]any, providerID string) string {
	provConfigs, ok := settings["provider"].(map[string]any)
	if !ok || len(provConfigs) == 0 {
		return ""
	}
	if _, found := provConfigs[providerID]; found {
		return providerID
	}
	if len(provConfigs) == 1 {
		for key := range provConfigs {
			return key
		}
	}
	return ""
}

// opencodeProviderConfig 从 SettingsConfig 中提取指定 provider 的 options 和 models。
func opencodeProviderConfig(settings map[string]any, providerID string) (options, models map[string]any) {
	key := opencodeProviderKey(settings, providerID)
	return opencodeProviderConfigByKey(settings, key)
}

func opencodeProviderConfigForProvider(provider Provider) (options, models map[string]any) {
	key := opencodeProviderKeyForProvider(provider)
	return opencodeProviderConfigByKey(provider.SettingsConfig, key)
}

func opencodeProviderConfigByKey(settings map[string]any, key string) (options, models map[string]any) {
	if key == "" {
		return map[string]any{}, map[string]any{}
	}
	if provConfigs, ok := settings["provider"].(map[string]any); ok {
		if cfg, ok := provConfigs[key].(map[string]any); ok {
			options, _ = cfg["options"].(map[string]any)
			models, _ = cfg["models"].(map[string]any)
		}
	}
	if options == nil {
		options = map[string]any{}
	}
	if models == nil {
		models = map[string]any{}
	}
	return
}

func opencodeProviderKeyForProvider(provider Provider) string {
	if key := stringValue(provider.Meta[opencodeProviderKeyMeta]); key != "" {
		return key
	}
	return opencodeProviderKey(provider.SettingsConfig, provider.ID)
}

func uniqueOpencodeProviderKey(name string, providers []Provider, live map[string]any) string {
	base := slugify(name)
	if base == "" {
		base = "provider"
	}
	used := map[string]struct{}{}
	for _, provider := range providers {
		used[provider.ID] = struct{}{}
		if key := opencodeProviderKeyForProvider(provider); key != "" {
			used[key] = struct{}{}
		}
	}
	if providerMap, ok := live["provider"].(map[string]any); ok {
		for key := range providerMap {
			used[key] = struct{}{}
		}
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

func firstMapKey(values map[string]any) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return ""
	}
	slices.Sort(keys)
	return keys[0]
}

func (s *Store) writeOpencodeLive(provider Provider) error {
	settings := CloneMap(provider.SettingsConfig)
	path := s.opencodeConfigPath()

	// 读取现有文件，保留其他 provider 和 $schema
	doc := map[string]any{}
	if content, err := os.ReadFile(path); err == nil {
		if err := unmarshalOpenCodeConfig(content, &doc); err != nil {
			return fmt.Errorf("解析 Opencode 配置失败: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取 Opencode 配置失败: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	if schema, ok := settings["$schema"]; ok {
		doc["$schema"] = schema
	}

	// 合并当前 provider 条目到文件
	docProviders := getOrCreateMap(doc, "provider")
	key := opencodeProviderKeyForProvider(provider)
	if key == "" {
		return fmt.Errorf("无法确定 OpenCode provider key: %s", provider.ID)
	}
	if srcProviders, ok := settings["provider"].(map[string]any); ok {
		if entry, ok := srcProviders[key]; ok {
			docProviders[key] = entry
		} else {
			return fmt.Errorf("OpenCode 配置缺少 provider 条目: %s", key)
		}
	} else {
		return fmt.Errorf("OpenCode 配置缺少 provider 表")
	}
	doc["provider"] = docProviders

	return writeJSONAtomic(path, doc)
}

func (s *Store) removeOpencodeProviderEntry(providerID string) error {
	path := s.opencodeConfigPath()
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读取 Opencode 配置失败: %w", err)
	}
	var doc map[string]any
	if err := unmarshalOpenCodeConfig(content, &doc); err != nil {
		return fmt.Errorf("解析 Opencode 配置失败: %w", err)
	}
	providers, ok := doc["provider"].(map[string]any)
	if !ok {
		return nil
	}
	delete(providers, providerID)
	doc["provider"] = providers
	if err := writeJSONAtomic(path, doc); err != nil {
		return fmt.Errorf("写入 Opencode 配置失败: %w", err)
	}
	return nil
}

func unmarshalOpenCodeConfig(data []byte, target *map[string]any) error {
	if err := json.Unmarshal(data, target); err == nil {
		return nil
	}
	return json.Unmarshal([]byte(stripJSONC(string(data))), target)
}

func stripJSONC(input string) string {
	var withoutComments strings.Builder
	inString := false
	escaped := false
	inLineComment := false
	inBlockComment := false
	for index := 0; index < len(input); index++ {
		char := input[index]
		if inLineComment {
			if char == '\n' {
				inLineComment = false
				withoutComments.WriteByte(char)
			}
			continue
		}
		if inBlockComment {
			if char == '*' && index+1 < len(input) && input[index+1] == '/' {
				inBlockComment = false
				index++
			}
			continue
		}
		if inString {
			withoutComments.WriteByte(char)
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
			withoutComments.WriteByte(char)
			continue
		}
		if char == '/' && index+1 < len(input) && input[index+1] == '/' {
			inLineComment = true
			index++
			continue
		}
		if char == '/' && index+1 < len(input) && input[index+1] == '*' {
			inBlockComment = true
			index++
			continue
		}
		withoutComments.WriteByte(char)
	}

	text := withoutComments.String()
	var result strings.Builder
	inString = false
	escaped = false
	for index := 0; index < len(text); index++ {
		char := text[index]
		if inString {
			result.WriteByte(char)
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
			result.WriteByte(char)
			continue
		}
		if char == ',' {
			next := index + 1
			for next < len(text) && strings.ContainsRune(" \t\r\n", rune(text[next])) {
				next++
			}
			if next < len(text) && (text[next] == '}' || text[next] == ']') {
				continue
			}
		}
		result.WriteByte(char)
	}
	return result.String()
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
	if custom := strings.TrimSpace(os.Getenv("OPENCODE_CONFIG")); custom != "" {
		return resolveOverridePath(custom)
	}
	dir := s.configDirFor(AppOpencode)
	jsonPath := filepath.Join(dir, "opencode.json")
	if fileExists(jsonPath) {
		return jsonPath
	}
	jsoncPath := filepath.Join(dir, "opencode.jsonc")
	if fileExists(jsoncPath) {
		return jsoncPath
	}
	return jsonPath
}

func (s *Store) refreshLiveHash(app AppType) error {
	if s.liveHash == nil {
		s.liveHash = map[AppType]string{}
	}
	data, err := s.liveFilesBytes(app)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.liveHash[app] = "missing"
			return nil
		}
		return err
	}
	hash := sha256.New()
	for _, item := range data {
		_, _ = hash.Write([]byte(item.path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(item.data)
		_, _ = hash.Write([]byte{0})
	}
	s.liveHash[app] = fmt.Sprintf("%x", hash.Sum(nil))
	return nil
}

func (s *Store) checkLiveHash(app AppType) error {
	if _, ok := s.liveHash[app]; !ok {
		return s.refreshLiveHash(app)
	}
	previous := s.liveHash[app]
	if err := s.refreshLiveHash(app); err != nil {
		return fmt.Errorf("检查 %s live 配置失败: %w", app.DisplayName(), err)
	}
	if previous != s.liveHash[app] {
		s.liveHash[app] = previous
		return fmt.Errorf("%s live 配置在 cctui 外部发生变化，请重新加载后再切换", app.DisplayName())
	}
	return nil
}

type liveFileBytes struct {
	path string
	data []byte
}

func (s *Store) liveFilesBytes(app AppType) ([]liveFileBytes, error) {
	paths := []string{}
	switch app {
	case AppClaude:
		paths = []string{s.claudeSettingsPath()}
	case AppCodex:
		paths = []string{s.codexAuthPath(), s.codexConfigPath()}
	case AppGemini:
		paths = []string{s.geminiEnvPath(), s.geminiSettingsPath()}
	case AppOpencode:
		paths = []string{s.opencodeConfigPath()}
	default:
		return nil, fmt.Errorf("不支持的应用类型: %s", app)
	}
	result := make([]liveFileBytes, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				result = append(result, liveFileBytes{path: path, data: []byte("<missing>")})
				continue
			}
			return nil, err
		}
		result = append(result, liveFileBytes{path: path, data: data})
	}
	return result, nil
}

func (s *Store) configDirFor(app AppType) string {
	if native := nativeConfigDir(app); native != "" {
		return native
	}
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
		if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
			return filepath.Join(resolveOverridePath(xdg), "opencode")
		}
		return filepath.Join(homeDir(), ".config", "opencode")
	default:
		return homeDir()
	}
}

func nativeConfigDir(app AppType) string {
	var envKey string
	switch app {
	case AppClaude:
		envKey = "CLAUDE_CONFIG_DIR"
	case AppCodex:
		envKey = "CODEX_HOME"
	case AppGemini:
		envKey = "GEMINI_CLI_HOME"
	case AppOpencode:
		envKey = "OPENCODE_CONFIG_DIR"
	}
	if envKey == "" {
		return ""
	}
	return resolveOverridePath(os.Getenv(envKey))
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

func parseCodexConfigFields(config string) (baseURL, model, reasoningEffort string, err error) {
	if strings.TrimSpace(config) == "" {
		return "", "", "", nil
	}
	doc := map[string]any{}
	if err := toml.Unmarshal([]byte(config), &doc); err != nil {
		return "", "", "", err
	}
	model = stringValue(doc["model"])
	reasoningEffort = stringValue(doc["model_reasoning_effort"])
	providerKey := strings.TrimSpace(stringValue(doc["model_provider"]))
	if providerKey != "" {
		if providers, ok := doc["model_providers"].(map[string]any); ok {
			if provider, ok := providers[providerKey].(map[string]any); ok {
				baseURL = firstNonEmpty(
					stringValue(provider["base_url"]),
					stringValue(provider["openai_base_url"]),
				)
			}
		}
	}
	if baseURL == "" {
		baseURL = firstNonEmpty(
			stringValue(doc["base_url"]),
			stringValue(doc["openai_base_url"]),
		)
	}
	return strings.TrimSpace(baseURL), strings.TrimSpace(model), strings.TrimSpace(reasoningEffort), nil
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
		return strings.TrimSpace(removeEmptyModelProvidersSection(string(buf))), nil
	}

	doc := map[string]any{}
	if err := toml.Unmarshal([]byte(existing), &doc); err != nil {
		return "", fmt.Errorf("解析 Codex config.toml 失败: %w", err)
	}

	patchGenericMapString(doc, "model", input.Model)
	patchGenericMapString(doc, "model_reasoning_effort", input.ReasoningEffort)
	// cctui must not disable Codex response storage. In addition to being an
	// unexpected side effect, that setting prevents Codex session recovery.
	delete(doc, "disable_response_storage")

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
		baseURLKey := "base_url"
		if _, exists := providerTable["openai_base_url"]; exists {
			baseURLKey = "openai_base_url"
		}
		patchGenericMapString(providerTable, baseURLKey, input.BaseURL)
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
		baseURLKey := "base_url"
		if _, exists := doc["openai_base_url"]; exists {
			baseURLKey = "openai_base_url"
		}
		patchGenericMapString(doc, baseURLKey, input.BaseURL)
	}

	buf, err := toml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("写回 Codex config.toml 失败: %w", err)
	}

	return strings.TrimSpace(removeEmptyModelProvidersSection(string(buf))), nil
}

func removeEmptyModelProvidersSection(config string) string {
	lines := strings.Split(config, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "[model_providers]" {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
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

func detectClaudeAPIKeyField(settings map[string]any) string {
	env := getOrCreateMap(settings, "env")
	if stringValue(env["ANTHROPIC_AUTH_TOKEN"]) != "" {
		return "ANTHROPIC_AUTH_TOKEN"
	}
	return "ANTHROPIC_API_KEY"
}

func detectGeminiAuthMode(settings map[string]any) string {
	env := getOrCreateMap(settings, "env")
	if stringValue(env["GEMINI_API_KEY"]) != "" {
		return geminiAuthModeAPIKey
	}
	if strings.EqualFold(stringValue(env["GOOGLE_GENAI_USE_VERTEXAI"]), "true") ||
		stringValue(env["GOOGLE_API_KEY"]) != "" ||
		stringValue(env["GOOGLE_CLOUD_PROJECT"]) != "" ||
		stringValue(env["GOOGLE_CLOUD_PROJECT_ID"]) != "" {
		return geminiAuthModeVertex
	}
	if config, ok := settings["config"].(map[string]any); ok {
		if selected := nestedStringValue(config, []string{"security", "auth", "selectedType"}); selected != "" {
			switch selected {
			case geminiAuthModeAPIKey, geminiAuthModeVertex, geminiAuthModeOAuth:
				return selected
			}
		}
	}
	return geminiAuthModeOAuth
}

func nestedStringValue(value map[string]any, path []string) string {
	current := value
	for index, key := range path {
		item, ok := current[key]
		if !ok {
			return ""
		}
		if index == len(path)-1 {
			return stringValue(item)
		}
		next, ok := item.(map[string]any)
		if !ok {
			return ""
		}
		current = next
	}
	return ""
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

func int64Ptr(value int64) *int64 {
	return &value
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
