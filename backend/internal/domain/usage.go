package domain

import "time"

const CodexSource = "provider.codex"

type UsageEvent struct {
	Source                   string
	DedupKey                 string
	OccurredAt               time.Time
	Model                    string
	Project                  string
	InputTokens              int64
	OutputTokens             int64
	CachedInputTokens        int64
	CacheCreationInputTokens int64
	ReasoningOutputTokens    int64
	ReportedTotalTokens      int64
	TotalTokens              int64
	RolloutKey               string
	ParentRolloutKey         string
	ReplayFingerprint        string
	InheritedReplay          bool
}

func (e UsageEvent) DetailTotal() int64 {
	return e.InputTokens +
		e.OutputTokens +
		e.CachedInputTokens +
		e.CacheCreationInputTokens +
		e.ReasoningOutputTokens
}

type SourceFileState struct {
	Source          string
	Path            string
	FileIdentity    string
	SizeBytes       int64
	MtimeNS         int64
	ParsedOffset    int64
	CompleteLineEnd int64
	HeadHash        string
	TailHash        string
	ParserVersion   int
	ParserStateJSON string
	LastSuccessAt   time.Time
	LastError       string
}

type UsageProviderState struct {
	Status      string
	LastScanAt  *time.Time
	LastError   string
	LastRunID   string
	LastRunMode string
	FilesSeen   int
	EventsSeen  int
}
