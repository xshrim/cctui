package codex

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeRolloutPath(t *testing.T) {
	t.Parallel()

	got := normalizeRolloutPath(`\\?\C:\Users\me\.codex\sessions\rollout.jsonl`)
	want := `C:\Users\me\.codex\sessions\rollout.jsonl`
	if got != want {
		t.Fatalf("normalizeRolloutPath() = %q, want %q", got, want)
	}
}

func TestOpenWorkspacePicksNewestStateDatabase(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	legacyDir := filepath.Join(dir, "sqlite")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	legacyPath := filepath.Join(legacyDir, "state_5.sqlite")
	currentPath := filepath.Join(dir, "state_5.sqlite")
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0o644); err != nil {
		t.Fatalf("WriteFile() legacy error = %v", err)
	}
	if err := os.WriteFile(currentPath, []byte("current"), 0o644); err != nil {
		t.Fatalf("WriteFile() current error = %v", err)
	}

	legacyTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	currentTime := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(legacyPath, legacyTime, legacyTime); err != nil {
		t.Fatalf("Chtimes() legacy error = %v", err)
	}
	if err := os.Chtimes(currentPath, currentTime, currentTime); err != nil {
		t.Fatalf("Chtimes() current error = %v", err)
	}

	workspace, err := OpenWorkspace(dir, false)
	if err != nil {
		t.Fatalf("OpenWorkspace() error = %v", err)
	}
	if workspace.DatabasePath != currentPath {
		t.Fatalf("OpenWorkspace() DatabasePath = %q, want %q", workspace.DatabasePath, currentPath)
	}
}

func TestUpdateSessionIndexCreatesEntry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "session_index.jsonl")
	workspace := &Workspace{SessionIndexPath: path}
	originalUpdatedAt := time.Date(2026, 5, 7, 7, 31, 10, 123000000, time.UTC)

	changed, err := workspace.updateSessionIndex([]Thread{{
		ID:          "thread-1",
		Title:       "Recovered Session",
		UpdatedAt:   originalUpdatedAt.Unix(),
		UpdatedAtMS: originalUpdatedAt.UnixMilli(),
	}}, false)
	if err != nil {
		t.Fatalf("updateSessionIndex() error = %v", err)
	}
	if !changed {
		t.Fatalf("updateSessionIndex() changed = false, want true")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"id":"thread-1"`) {
		t.Fatalf("session index missing thread id: %s", text)
	}
	if !strings.Contains(text, `"thread_name":"Recovered Session"`) {
		t.Fatalf("session index missing thread title: %s", text)
	}
	if !strings.Contains(text, `"updated_at":"2026-05-07T07:31:10.123Z"`) {
		t.Fatalf("session index did not preserve original updated_at: %s", text)
	}
}

func TestParseModelProvider(t *testing.T) {
	t.Parallel()

	config := `
# comment
model_provider = "custom" # current provider

[model_providers.custom]
name = "Custom"
`
	if got := parseModelProvider(config); got != "custom" {
		t.Fatalf("parseModelProvider() = %q, want custom", got)
	}
}

func TestParseModelProviderFallsBackToSingleProfile(t *testing.T) {
	t.Parallel()

	config := `
service_tier = "priority"

[profiles.m21]
model = "codex-MiniMax-M2.7"
model_provider = "minimax"
`
	if got := parseModelProvider(config); got != "minimax" {
		t.Fatalf("parseModelProvider() = %q, want minimax", got)
	}
}

func TestPatchRolloutMetadataUpdatesAllProviderFields(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	original := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"thread-1","cwd":"/old","model_provider":"old_provider"}}`,
		`{"type":"event","payload":{"items":[{"model_provider":"old_provider"},{"model_provider":"current"}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	originalModTime := time.Date(2026, 4, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(path, originalModTime, originalModTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	changed, err := patchRolloutMetadata(path, "thread-1", "current", "/old", false)
	if err != nil {
		t.Fatalf("patchRolloutMetadata() error = %v", err)
	}
	if !changed {
		t.Fatalf("patchRolloutMetadata() changed = false, want true")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(data)
	if strings.Contains(text, "old_provider") {
		t.Fatalf("rollout still contains old provider: %s", text)
	}
	if got := strings.Count(text, `"model_provider":"current"`); got != 3 {
		t.Fatalf("current provider count = %d, want 3 in %s", got, text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if !info.ModTime().Equal(originalModTime) {
		t.Fatalf("rollout mod time = %s, want %s", info.ModTime(), originalModTime)
	}
}

func TestProviderMismatchThreadsIncludesOfficialSessions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state_5.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		create table threads (
			id text primary key,
			rollout_path text not null,
			created_at integer not null,
			updated_at integer not null,
			source text not null,
			model_provider text not null,
			cwd text not null,
			title text not null,
			sandbox_policy text not null,
			approval_mode text not null,
			tokens_used integer not null default 0,
			has_user_event integer not null default 0,
			archived integer not null default 0,
			archived_at integer,
			git_sha text,
			git_branch text,
			git_origin_url text,
			cli_version text not null default '',
			first_user_message text not null default '',
			agent_nickname text,
			agent_role text,
			memory_mode text not null default 'enabled',
			model text,
			reasoning_effort text,
			agent_path text,
			created_at_ms integer,
			updated_at_ms integer,
			thread_source text,
			preview text not null default ''
		)
	`)
	if err != nil {
		t.Fatalf("create table error = %v", err)
	}

	officialRollout := filepath.Join(dir, "official.jsonl")
	customRollout := filepath.Join(dir, "custom.jsonl")
	if err := os.WriteFile(officialRollout, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() official error = %v", err)
	}
	if err := os.WriteFile(customRollout, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() custom error = %v", err)
	}

	now := time.Now().Unix()
	_, err = db.Exec(`
		insert into threads (
			id, rollout_path, created_at, updated_at, source, model_provider, cwd, title,
			sandbox_policy, approval_mode, tokens_used, has_user_event, archived, cli_version,
			first_user_message, memory_mode, created_at_ms, updated_at_ms, thread_source, preview
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "official-thread", officialRollout, now, now, "vscode", "openai", dir, "Official", "workspace-write", "accept", 0, 1, 0, "0.140.0-alpha.19", "", "enabled", now*1000, now*1000, "user", "")
	if err != nil {
		t.Fatalf("insert official thread error = %v", err)
	}
	_, err = db.Exec(`
		insert into threads (
			id, rollout_path, created_at, updated_at, source, model_provider, cwd, title,
			sandbox_policy, approval_mode, tokens_used, has_user_event, archived, cli_version,
			first_user_message, memory_mode, created_at_ms, updated_at_ms, thread_source, preview
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "custom-thread", customRollout, now-10, now-10, "vscode", "minimax", dir, "Custom", "workspace-write", "accept", 0, 1, 0, "0.140.0-alpha.19", "", "enabled", (now-10)*1000, (now-10)*1000, "user", "")
	if err != nil {
		t.Fatalf("insert custom thread error = %v", err)
	}

	workspace := &Workspace{DatabasePath: dbPath}
	threads, err := workspace.ProviderMismatchThreads("my_codex", false, 0)
	if err != nil {
		t.Fatalf("ProviderMismatchThreads() error = %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("ProviderMismatchThreads() len = %d, want 2", len(threads))
	}
	if threads[0].ID != "official-thread" && threads[1].ID != "official-thread" {
		t.Fatalf("ProviderMismatchThreads() did not include official-thread: %#v", threads)
	}
	if threads[0].ID != "custom-thread" && threads[1].ID != "custom-thread" {
		t.Fatalf("ProviderMismatchThreads() did not include custom-thread: %#v", threads)
	}
}

func TestRepairThreadsSkipsMissingRolloutAndContinues(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state_5.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		create table threads (
			id text primary key,
			rollout_path text not null,
			created_at integer not null,
			updated_at integer not null,
			source text not null,
			model_provider text not null,
			cwd text not null,
			title text not null,
			sandbox_policy text not null,
			approval_mode text not null,
			tokens_used integer not null default 0,
			has_user_event integer not null default 0,
			archived integer not null default 0,
			archived_at integer,
			git_sha text,
			git_branch text,
			git_origin_url text,
			cli_version text not null default '',
			first_user_message text not null default '',
			agent_nickname text,
			agent_role text,
			memory_mode text not null default 'enabled',
			model text,
			reasoning_effort text,
			agent_path text,
			created_at_ms integer,
			updated_at_ms integer,
			thread_source text,
			preview text not null default ''
		)
	`)
	if err != nil {
		t.Fatalf("create table error = %v", err)
	}

	validRollout := filepath.Join(dir, "valid.jsonl")
	if err := os.WriteFile(validRollout, []byte(`{"type":"session_meta","payload":{"id":"valid","cwd":"/old","model_provider":"minimax"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() valid error = %v", err)
	}

	now := time.Now().Unix()
	_, err = db.Exec(`
		insert into threads (
			id, rollout_path, created_at, updated_at, source, model_provider, cwd, title,
			sandbox_policy, approval_mode, tokens_used, has_user_event, archived, cli_version,
			first_user_message, memory_mode, created_at_ms, updated_at_ms, thread_source, preview
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "missing-thread", filepath.Join(dir, "missing.jsonl"), now, now, "vscode", "minimax", dir, "Missing", "workspace-write", "accept", 0, 1, 0, "0.140.0-alpha.19", "", "enabled", now*1000, now*1000, "user", "")
	if err != nil {
		t.Fatalf("insert missing thread error = %v", err)
	}
	_, err = db.Exec(`
		insert into threads (
			id, rollout_path, created_at, updated_at, source, model_provider, cwd, title,
			sandbox_policy, approval_mode, tokens_used, has_user_event, archived, cli_version,
			first_user_message, memory_mode, created_at_ms, updated_at_ms, thread_source, preview
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "valid-thread", validRollout, now-10, now-10, "vscode", "minimax", dir, "Valid", "workspace-write", "accept", 0, 1, 0, "0.140.0-alpha.19", "", "enabled", (now-10)*1000, (now-10)*1000, "user", "")
	if err != nil {
		t.Fatalf("insert valid thread error = %v", err)
	}

	workspace := &Workspace{DatabasePath: dbPath}
	report, err := workspace.RepairThreads(RepairOptions{
		ThreadIDs:     []string{"missing-thread", "valid-thread"},
		ModelProvider: "openai",
		DryRun:        false,
	})
	if err != nil {
		t.Fatalf("RepairThreads() error = %v", err)
	}
	if len(report.SkippedThreads) != 1 {
		t.Fatalf("SkippedThreads len = %d, want 1", len(report.SkippedThreads))
	}
	if report.SkippedThreads[0].ID != "missing-thread" {
		t.Fatalf("SkippedThreads[0].ID = %q, want missing-thread", report.SkippedThreads[0].ID)
	}
	if len(report.Threads) != 1 {
		t.Fatalf("Repaired threads len = %d, want 1", len(report.Threads))
	}
	if report.Threads[0].ID != "valid-thread" {
		t.Fatalf("RepairedThreads[0].ID = %q, want valid-thread", report.Threads[0].ID)
	}
}
