package codex

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var sqliteCandidates = []string{
	filepath.Join("sqlite", "state_5.sqlite"),
	"state_5.sqlite",
}

type Workspace struct {
	CodexDir         string
	DatabasePath     string
	ConfigPath       string
	SessionIndexPath string
	DryRun           bool
}

func DefaultCodexDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

func OpenWorkspace(codexDir string, dryRun bool) (*Workspace, error) {
	if codexDir == "" {
		codexDir = DefaultCodexDir()
	}
	cleanDir, err := filepath.Abs(codexDir)
	if err != nil {
		return nil, fmt.Errorf("resolve codex dir: %w", err)
	}

	var dbPath string
	var dbInfo os.FileInfo
	for _, candidate := range sqliteCandidates {
		path := filepath.Join(cleanDir, candidate)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if dbPath == "" || info.ModTime().After(dbInfo.ModTime()) {
			dbPath = path
			dbInfo = info
		}
	}
	if dbPath == "" {
		return nil, fmt.Errorf("state_5.sqlite not found under %s", cleanDir)
	}

	return &Workspace{
		CodexDir:         cleanDir,
		DatabasePath:     dbPath,
		ConfigPath:       filepath.Join(cleanDir, "config.toml"),
		SessionIndexPath: filepath.Join(cleanDir, "session_index.jsonl"),
		DryRun:           dryRun,
	}, nil
}

func (w *Workspace) db() (*sql.DB, error) {
	db, err := sql.Open("sqlite", w.DatabasePath)
	if err != nil {
		return nil, err
	}
	return db, nil
}

func (w *Workspace) RecentThreads(limit int) ([]Thread, error) {
	if limit <= 0 {
		limit = 20
	}
	db, err := w.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`
		select
			id,
			coalesce(title, ''),
			coalesce(cwd, ''),
			coalesce(model_provider, ''),
			coalesce(archived, 0),
			coalesce(has_user_event, 0),
			coalesce(rollout_path, ''),
			coalesce(updated_at, 0),
			coalesce(updated_at_ms, 0),
			coalesce(source, ''),
			coalesce(cli_version, '')
		from threads
		order by updated_at desc
		limit ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []Thread
	for rows.Next() {
		var thread Thread
		if err := rows.Scan(
			&thread.ID,
			&thread.Title,
			&thread.CWD,
			&thread.ModelProvider,
			&thread.Archived,
			&thread.HasUserEvent,
			&thread.RolloutPath,
			&thread.UpdatedAt,
			&thread.UpdatedAtMS,
			&thread.Source,
			&thread.CLIVersion,
		); err != nil {
			return nil, err
		}
		threads = append(threads, thread)
	}
	return threads, rows.Err()
}

func (w *Workspace) ThreadByID(id string) (Thread, error) {
	db, err := w.db()
	if err != nil {
		return Thread{}, err
	}
	defer db.Close()

	var thread Thread
	err = db.QueryRow(`
		select
			id,
			coalesce(title, ''),
			coalesce(cwd, ''),
			coalesce(model_provider, ''),
			coalesce(archived, 0),
			coalesce(has_user_event, 0),
			coalesce(rollout_path, ''),
			coalesce(updated_at, 0),
			coalesce(updated_at_ms, 0),
			coalesce(source, ''),
			coalesce(cli_version, '')
		from threads
		where id = ?
	`, id).Scan(
		&thread.ID,
		&thread.Title,
		&thread.CWD,
		&thread.ModelProvider,
		&thread.Archived,
		&thread.HasUserEvent,
		&thread.RolloutPath,
		&thread.UpdatedAt,
		&thread.UpdatedAtMS,
		&thread.Source,
		&thread.CLIVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Thread{}, fmt.Errorf("thread %q not found", id)
	}
	return thread, err
}

func (w *Workspace) CurrentModelProvider() (string, error) {
	data, err := os.ReadFile(w.ConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "openai", nil
		}
		return "", err
	}
	provider := parseModelProvider(string(data))
	if provider == "" {
		return "openai", nil
	}
	return provider, nil
}

func (w *Workspace) ProviderMismatchThreads(provider string, includeArchived bool, limit int) ([]Thread, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, errors.New("provider is required")
	}

	db, err := w.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	where := []string{
		"coalesce(model_provider, '') <> ?",
		"coalesce(rollout_path, '') <> ''",
	}
	args := []any{provider}
	if !includeArchived {
		where = append(where, "coalesce(archived, 0) = 0")
	}

	limitSQL := ""
	if limit > 0 {
		limitSQL = " limit ?"
		args = append(args, limit)
	}

	rows, err := db.Query(`
		select
			id,
			coalesce(title, ''),
			coalesce(cwd, ''),
			coalesce(model_provider, ''),
			coalesce(archived, 0),
			coalesce(has_user_event, 0),
			coalesce(rollout_path, ''),
			coalesce(updated_at, 0),
			coalesce(updated_at_ms, 0),
			coalesce(source, ''),
			coalesce(cli_version, '')
		from threads
		where `+strings.Join(where, " and ")+`
		order by updated_at desc`+limitSQL,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []Thread
	for rows.Next() {
		var thread Thread
		if err := rows.Scan(
			&thread.ID,
			&thread.Title,
			&thread.CWD,
			&thread.ModelProvider,
			&thread.Archived,
			&thread.HasUserEvent,
			&thread.RolloutPath,
			&thread.UpdatedAt,
			&thread.UpdatedAtMS,
			&thread.Source,
			&thread.CLIVersion,
		); err != nil {
			return nil, err
		}
		threads = append(threads, thread)
	}
	return threads, rows.Err()
}

func (w *Workspace) SearchTranscripts(keywords []string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	needles := normalizeKeywords(keywords)
	if len(needles) == 0 {
		return nil, errors.New("at least one keyword is required")
	}

	var roots []string
	for _, dir := range []string{"sessions", "archived_sessions"} {
		root := filepath.Join(w.CodexDir, dir)
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			roots = append(roots, root)
		}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("no session directories found under %s", w.CodexDir)
	}

	results := make([]SearchResult, 0, limit)
	stop := errors.New("search limit reached")

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || filepath.Ext(path) != ".jsonl" {
				return nil
			}
			fileResults, err := searchFile(path, needles, limit-len(results))
			if err != nil {
				return err
			}
			results = append(results, fileResults...)
			if len(results) >= limit {
				return stop
			}
			return nil
		})
		if err != nil && !errors.Is(err, stop) {
			return nil, err
		}
		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

func (w *Workspace) RepairThreads(opts RepairOptions) (*RepairReport, error) {
	if len(opts.ThreadIDs) == 0 {
		return nil, errors.New("at least one thread id is required")
	}
	threadIDs := uniqueStrings(opts.ThreadIDs)

	db, err := w.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	targets := make([]Thread, 0, len(threadIDs))
	for _, id := range threadIDs {
		thread, err := w.ThreadByID(id)
		if err != nil {
			return nil, err
		}
		targets = append(targets, thread)
	}

	if opts.ReferenceThreadID != "" {
		reference, err := w.ThreadByID(opts.ReferenceThreadID)
		if err != nil {
			return nil, fmt.Errorf("load reference thread: %w", err)
		}
		if opts.ModelProvider == "" {
			opts.ModelProvider = reference.ModelProvider
		}
		if opts.CWD == "" {
			opts.CWD = reference.CWD
		}
	}

	backupSet := map[string]struct{}{}
	report := &RepairReport{
		DryRun:           opts.DryRun,
		DatabasePath:     w.DatabasePath,
		TargetProvider:   opts.ModelProvider,
		SessionIndexPath: w.SessionIndexPath,
	}
	stamp := time.Now().UTC().Format("20060102_150405")

	if err := w.backupOnce(w.DatabasePath, stamp, backupSet, report); err != nil {
		return nil, err
	}
	if _, err := os.Stat(w.SessionIndexPath); err == nil {
		if err := w.backupOnce(w.SessionIndexPath, stamp, backupSet, report); err != nil {
			return nil, err
		}
	}

	updatedThreads := make([]Thread, 0, len(targets))
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, thread := range targets {
		targetProvider := firstNonEmpty(opts.ModelProvider, thread.ModelProvider)
		targetCWD := firstNonEmpty(opts.CWD, thread.CWD)
		rolloutPath := normalizeRolloutPath(thread.RolloutPath)
		if rolloutPath == "" {
			report.SkippedThreads = append(report.SkippedThreads, SkippedThread{
				ID:     thread.ID,
				Title:  thread.Title,
				Reason: "empty rollout_path",
			})
			continue
		}
		if _, err := os.Stat(rolloutPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				report.SkippedThreads = append(report.SkippedThreads, SkippedThread{
					ID:          thread.ID,
					Title:       thread.Title,
					RolloutPath: rolloutPath,
					Reason:      "rollout file missing",
				})
				continue
			}
			return nil, fmt.Errorf("stat rollout %s: %w", rolloutPath, err)
		}

		if err := w.backupOnce(rolloutPath, stamp, backupSet, report); err != nil {
			return nil, err
		}
		changed, err := patchRolloutMetadata(rolloutPath, thread.ID, targetProvider, targetCWD, opts.DryRun)
		if err != nil {
			return nil, fmt.Errorf("patch rollout %s: %w", rolloutPath, err)
		}

		if !opts.DryRun {
			if _, err := tx.Exec(`
				update threads
				set
					model_provider = ?,
					cwd = ?,
					has_user_event = 0,
					archived = 0
				where id = ?
			`, targetProvider, targetCWD, thread.ID); err != nil {
				return nil, fmt.Errorf("update thread %s: %w", thread.ID, err)
			}
		}

		thread.ModelProvider = targetProvider
		thread.CWD = targetCWD
		thread.Archived = 0
		thread.HasUserEvent = 0
		thread.RolloutPath = rolloutPath
		updatedThreads = append(updatedThreads, thread)

		report.Threads = append(report.Threads, RepairedThread{
			ID:              thread.ID,
			Title:           thread.Title,
			RolloutPath:     rolloutPath,
			UpdatedProvider: targetProvider,
			UpdatedCWD:      targetCWD,
			RolloutChanged:  changed,
		})
	}

	if !opts.DryRun {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		committed = true
		if _, err := db.Exec(`pragma wal_checkpoint(full)`); err != nil {
			return nil, fmt.Errorf("wal checkpoint: %w", err)
		}
		if _, err := db.Exec(`pragma integrity_check`); err != nil {
			return nil, fmt.Errorf("integrity check: %w", err)
		}
	}

	updated, err := w.updateSessionIndex(updatedThreads, opts.DryRun)
	if err != nil {
		return nil, err
	}
	report.SessionIndexUpdated = updated
	for i := range report.Threads {
		report.Threads[i].SessionIndexChange = updated
	}

	return report, nil
}

func (w *Workspace) SwitchToCurrentProvider(opts SwitchProviderOptions) (*RepairReport, error) {
	provider, err := w.CurrentModelProvider()
	if err != nil {
		return nil, fmt.Errorf("read current provider: %w", err)
	}
	threads, err := w.ProviderMismatchThreads(provider, opts.IncludeArchived, opts.Limit)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(threads))
	for _, thread := range threads {
		ids = append(ids, thread.ID)
	}
	if len(ids) == 0 {
		return &RepairReport{
			DryRun:           opts.DryRun,
			DatabasePath:     w.DatabasePath,
			TargetProvider:   provider,
			SessionIndexPath: w.SessionIndexPath,
		}, nil
	}

	return w.RepairThreads(RepairOptions{
		ThreadIDs:     ids,
		ModelProvider: provider,
		DryRun:        opts.DryRun,
	})
}

func (w *Workspace) backupOnce(path, stamp string, seen map[string]struct{}, report *RepairReport) error {
	if _, ok := seen[path]; ok {
		return nil
	}
	seen[path] = struct{}{}

	backupPath := path + ".bak.session-restore-" + stamp
	report.BackupPaths = append(report.BackupPaths, backupPath)
	if w.DryRun {
		return nil
	}
	if err := copyFile(path, backupPath); err != nil {
		return fmt.Errorf("backup %s: %w", path, err)
	}
	return nil
}

func searchFile(path string, keywords []string, remaining int) ([]SearchResult, error) {
	if remaining <= 0 {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	lineNumber := 0
	results := make([]SearchResult, 0, remaining)

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNumber++
			text := strings.TrimRight(string(line), "\r\n")
			lower := strings.ToLower(text)
			if containsAll(lower, keywords) {
				results = append(results, SearchResult{
					Path:       path,
					LineNumber: lineNumber,
					Line:       trimLine(text, 220),
				})
				if len(results) >= remaining {
					return results, nil
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return results, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func patchRolloutMetadata(path, threadID, provider, cwd string, dryRun bool) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return false, err
	}

	reader := bufio.NewReader(file)
	tempPath := path + ".tmp"
	var tempFile *os.File
	if !dryRun {
		tempFile, err = os.Create(tempPath)
		if err != nil {
			return false, err
		}
		defer func() {
			_ = tempFile.Close()
			_ = os.Remove(tempPath)
		}()
	}

	lineNumber := 0
	changed := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNumber++
			patchedLine, lineChanged, err := patchRolloutLine(line, lineNumber == 1, threadID, provider, cwd)
			if err != nil {
				return false, err
			}
			if lineChanged {
				changed = true
				line = patchedLine
			}
			if tempFile != nil {
				if _, err := tempFile.Write(line); err != nil {
					return false, err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return false, readErr
		}
	}

	if lineNumber == 0 {
		return false, fmt.Errorf("rollout is empty")
	}
	if !changed {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if err := tempFile.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tempPath, info.Mode()); err != nil {
		return false, err
	}
	if err := os.Chtimes(tempPath, info.ModTime(), info.ModTime()); err != nil {
		return false, err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return false, err
	}
	return true, nil
}

func patchRolloutLine(line []byte, firstLine bool, threadID, provider, cwd string) ([]byte, bool, error) {
	body, ending := splitLineEnding(line)
	if len(bytes.TrimSpace(body)) == 0 {
		if firstLine {
			return nil, false, fmt.Errorf("first line is empty")
		}
		return line, false, nil
	}
	shouldParse := firstLine || (provider != "" && bytes.Contains(body, []byte(`"model_provider"`)))
	if !shouldParse {
		return line, false, nil
	}

	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		if firstLine {
			return nil, false, fmt.Errorf("decode first line json: %w", err)
		}
		return line, false, nil
	}

	changed := false
	if provider != "" && updateModelProviderFields(document, provider) > 0 {
		changed = true
	}

	if firstLine {
		payload, ok := document["payload"].(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("first line payload is missing or not an object")
		}
		if threadID != "" && stringValue(payload["id"]) != threadID {
			payload["id"] = threadID
			changed = true
		}
		if cwd != "" && stringValue(payload["cwd"]) != cwd {
			payload["cwd"] = cwd
			changed = true
		}
	}

	if !changed {
		return line, false, nil
	}

	updatedLine, err := json.Marshal(document)
	if err != nil {
		return nil, false, fmt.Errorf("encode rollout json: %w", err)
	}
	updatedLine = append(updatedLine, ending...)
	return updatedLine, true, nil
}

func updateModelProviderFields(value any, provider string) int {
	changed := 0
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if key == "model_provider" {
				if current, ok := item.(string); ok && current != provider {
					typed[key] = provider
					changed++
				}
				continue
			}
			changed += updateModelProviderFields(item, provider)
		}
	case []any:
		for _, item := range typed {
			changed += updateModelProviderFields(item, provider)
		}
	}
	return changed
}

func splitLineEnding(line []byte) ([]byte, []byte) {
	if bytes.HasSuffix(line, []byte("\r\n")) {
		return line[:len(line)-2], line[len(line)-2:]
	}
	if bytes.HasSuffix(line, []byte("\n")) {
		return line[:len(line)-1], line[len(line)-1:]
	}
	return line, nil
}

type sessionIndexEntry struct {
	ID         string `json:"id"`
	ThreadName string `json:"thread_name"`
	UpdatedAt  string `json:"updated_at"`
}

func (w *Workspace) updateSessionIndex(threads []Thread, dryRun bool) (bool, error) {
	if strings.TrimSpace(w.SessionIndexPath) == "" {
		return false, nil
	}
	entries, err := readSessionIndexEntries(w.SessionIndexPath)
	if err != nil {
		return false, err
	}

	indexByID := make(map[string]int, len(entries))
	for i, entry := range entries {
		indexByID[entry.ID] = i
	}

	changed := false
	for _, thread := range threads {
		entry := sessionIndexEntry{
			ID:         thread.ID,
			ThreadName: firstNonEmpty(thread.Title, thread.ID),
			UpdatedAt:  threadUpdatedAtISO(thread),
		}
		if idx, ok := indexByID[thread.ID]; ok {
			if entries[idx] != entry {
				entries[idx] = entry
				changed = true
			}
			continue
		}
		entries = append(entries, entry)
		indexByID[thread.ID] = len(entries) - 1
		changed = true
	}

	if !changed {
		return false, nil
	}
	if dryRun {
		return true, nil
	}

	var builder strings.Builder
	for _, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			return false, err
		}
		builder.Write(line)
		builder.WriteByte('\n')
	}
	if err := os.WriteFile(w.SessionIndexPath, []byte(builder.String()), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func readSessionIndexEntries(path string) ([]sessionIndexEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)
	entries := make([]sessionIndexEntry, 0)
	seen := map[string]struct{}{}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry sessionIndexEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("decode session index line: %w", err)
		}
		if entry.ID == "" {
			continue
		}
		if _, ok := seen[entry.ID]; ok {
			continue
		}
		seen[entry.ID] = struct{}{}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

func copyFile(src, dst string) error {
	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer input.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	output, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer output.Close()

	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err == nil {
		_ = os.Chmod(dst, info.Mode())
	}
	return output.Close()
}

func normalizeKeywords(keywords []string) []string {
	var result []string
	for _, keyword := range keywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword == "" {
			continue
		}
		if !slices.Contains(result, keyword) {
			result = append(result, keyword)
		}
	}
	return result
}

func containsAll(text string, keywords []string) bool {
	for _, keyword := range keywords {
		if !strings.Contains(text, keyword) {
			return false
		}
	}
	return true
}

func trimLine(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	return text[:max-3] + "..."
}

func threadUpdatedAtISO(thread Thread) string {
	if thread.UpdatedAtMS > 0 {
		return time.UnixMilli(thread.UpdatedAtMS).UTC().Format(time.RFC3339Nano)
	}
	if thread.UpdatedAt > 0 {
		return time.Unix(thread.UpdatedAt, 0).UTC().Format(time.RFC3339Nano)
	}
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func normalizeRolloutPath(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(path, `\\?\UNC\`)
	}
	if strings.HasPrefix(path, `\\?\`) {
		return strings.TrimPrefix(path, `\\?\`)
	}
	return path
}

func parseModelProvider(config string) string {
	section := ""
	var profileProviders []string
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.Contains(line, "]") {
			section = line
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "model_provider" {
			continue
		}
		value = stripInlineComment(strings.TrimSpace(value))
		provider := ""
		if unquoted, err := strconv.Unquote(value); err == nil {
			provider = strings.TrimSpace(unquoted)
		} else {
			provider = strings.Trim(value, `"' `)
		}
		if section == "" {
			return provider
		}
		if strings.HasPrefix(section, "[profiles.") && provider != "" && !slices.Contains(profileProviders, provider) {
			profileProviders = append(profileProviders, provider)
		}
	}
	if len(profileProviders) == 1 {
		return profileProviders[0]
	}
	return ""
}

func stripInlineComment(value string) string {
	inQuote := rune(0)
	escaped := false
	for i, char := range value {
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' && inQuote != 0 {
			escaped = true
			continue
		}
		if char == '\'' || char == '"' {
			if inQuote == 0 {
				inQuote = char
				continue
			}
			if inQuote == char {
				inQuote = 0
			}
			continue
		}
		if char == '#' && inQuote == 0 {
			return strings.TrimSpace(value[:i])
		}
	}
	return strings.TrimSpace(value)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func uniqueStrings(values []string) []string {
	var result []string
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
