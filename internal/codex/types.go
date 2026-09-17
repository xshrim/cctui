package codex

type Thread struct {
	ID            string
	Title         string
	CWD           string
	ModelProvider string
	Archived      int
	HasUserEvent  int
	RolloutPath   string
	UpdatedAt     int64
	UpdatedAtMS   int64
	Source        string
	CLIVersion    string
}

type SearchResult struct {
	Path       string
	LineNumber int
	Line       string
}

type RepairOptions struct {
	ThreadIDs         []string
	ReferenceThreadID string
	ModelProvider     string
	CWD               string
	DryRun            bool
}

type SwitchProviderOptions struct {
	IncludeArchived bool
	Limit           int
	DryRun          bool
}

type RepairedThread struct {
	ID                 string
	Title              string
	RolloutPath        string
	UpdatedProvider    string
	UpdatedCWD         string
	RolloutChanged     bool
	SessionIndexChange bool
}

type SkippedThread struct {
	ID          string
	Title       string
	RolloutPath string
	Reason      string
}

type RepairReport struct {
	DryRun              bool
	DatabasePath        string
	TargetProvider      string
	SessionIndexPath    string
	BackupPaths         []string
	Threads             []RepairedThread
	SkippedThreads      []SkippedThread
	SessionIndexUpdated bool
}
