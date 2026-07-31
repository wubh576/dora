package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/provider/claudecode"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

type claudeScanner struct {
	store  *dorasqlite.Store
	homes  []string
	parser claudecode.Parser
	now    func() time.Time
}

type claudeFileTask struct {
	file     claudecode.File
	metadata claudecode.FileMetadata
	offset   int64
	state    claudecode.ParserState
}

func newClaudeScanner(store *dorasqlite.Store, homes []string) *claudeScanner {
	return &claudeScanner{
		store: store, homes: append([]string(nil), homes...),
		parser: claudecode.NewParser(), now: time.Now,
	}
}

func (s *claudeScanner) scan(ctx context.Context, forceFull bool) (report Report, returnErr error) {
	startedAt := s.now().UTC()
	runID, err := newRunID()
	if err != nil {
		return Report{}, err
	}
	report.RunID = runID
	report.Mode = "planning"
	if err := s.store.BeginProviderUsageScan(ctx, domain.ClaudeCodeSource, runID, report.Mode, startedAt); err != nil {
		return report, err
	}
	defer func() {
		if returnErr == nil {
			return
		}
		finishedAt := s.now().UTC()
		if err := s.store.FailProviderUsageScan(
			context.Background(), domain.ClaudeCodeSource, runID, finishedAt,
			report.FilesSeen, report.EventsSeen, returnErr,
		); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()

	homes, err := claudecode.ResolveHomes(s.homes)
	if err != nil {
		return report, safeClaudeError("解析配置目录", err)
	}
	files, err := claudecode.Discover(homes)
	if err != nil {
		return report, safeClaudeError("发现 transcript", err)
	}
	report.FilesSeen = len(files)
	report.SessionCount = claudecode.SessionCount(files)
	configFound := directoryFound(homes)
	previous, err := s.store.LoadSourceFiles(ctx, domain.ClaudeCodeSource)
	if err != nil {
		return report, err
	}
	providerState, err := s.store.UsageProviderState(ctx, domain.ClaudeCodeSource)
	if err != nil {
		return report, err
	}
	if !configFound || len(files) == 0 && (len(previous) > 0 || providerState.StoredEvents > 0) {
		report.Mode = "preserve"
		report.EventsStored = providerState.StoredEvents
		report.FinishedAt = s.now().UTC()
		if err := s.store.UpdateUsageScanMode(ctx, runID, report.Mode); err != nil {
			return report, err
		}
		status := "ready"
		if !configFound {
			status = "not_found"
		}
		if err := s.store.CompleteProviderUsageScanWithoutReplacement(
			ctx, domain.ClaudeCodeSource, runID, report.FinishedAt,
			report.FilesSeen, report.EventsSeen, "",
			domain.UsageScanMetrics{
				Status: status, ConfigFound: configFound,
				SessionCount: report.SessionCount, ParserVersion: claudecode.ParserVersion,
			},
		); err != nil {
			return report, err
		}
		return report, nil
	}
	lastFull, err := s.store.LastSuccessfulFullScan(ctx, domain.ClaudeCodeSource)
	if err != nil {
		return report, err
	}
	mode, tasks, states, err := s.plan(ctx, files, previous, lastFull, forceFull, startedAt)
	if err != nil {
		return report, err
	}
	report.Mode = mode
	if err := s.store.UpdateUsageScanMode(ctx, runID, mode); err != nil {
		return report, err
	}

	events := make([]domain.UsageEvent, 0)
	if mode != "full" {
		events, err = s.store.LoadUsageEvents(ctx, domain.ClaudeCodeSource)
		if err != nil {
			return report, err
		}
	}
	for _, task := range tasks {
		result, err := s.parser.ParseFileSnapshot(ctx, task.file, task.offset, task.metadata.Size, task.state)
		if err != nil {
			return report, safeClaudeError("解析 transcript", err)
		}
		valid, err := claudecode.MatchesSnapshot(task.file, task.metadata)
		if err != nil {
			return report, safeClaudeError("校验 transcript 快照", err)
		}
		if !valid {
			return report, errors.New("Claude Code transcript 在扫描期间发生非追加变化")
		}
		report.EventsSeen += len(result.Events)
		events = append(events, result.Events...)
		report.Warnings = append(report.Warnings, result.Warnings...)
		stateJSON, err := json.Marshal(result.State)
		if err != nil {
			return report, fmt.Errorf("保存 Claude Code parser 状态: %w", err)
		}
		checkpointKey := claudecode.CheckpointKey(task.file)
		states[checkpointKey] = domain.SourceFileState{
			Source: domain.ClaudeCodeSource, Path: checkpointKey,
			FileIdentity: task.metadata.Identity, SizeBytes: task.metadata.Size,
			MtimeNS: task.metadata.MtimeNS, ParsedOffset: result.CompleteLineEnd,
			CompleteLineEnd: result.CompleteLineEnd, HeadHash: task.metadata.HeadHash,
			TailHash: task.metadata.TailHash, ParserVersion: claudecode.ParserVersion,
			ParserStateJSON: string(stateJSON), LastSuccessAt: startedAt,
		}
	}

	events = claudecode.Reconcile(events)
	report.EventsStored = len(events)
	report.Warnings = uniqueStrings(report.Warnings)
	report.FinishedAt = s.now().UTC()
	orderedStates := make([]domain.SourceFileState, 0, len(files))
	for _, file := range files {
		orderedStates = append(orderedStates, states[claudecode.CheckpointKey(file)])
	}
	status := "ready"
	if !configFound {
		status = "not_found"
	} else if len(report.Warnings) > 0 {
		status = "degraded"
	}
	if err := s.store.CompleteProviderUsageScanWithMetrics(
		ctx, domain.ClaudeCodeSource, runID, report.FinishedAt, events, orderedStates,
		report.FilesSeen, report.EventsSeen, strings.Join(report.Warnings, "；"),
		domain.UsageScanMetrics{
			Status: status, ConfigFound: configFound,
			SessionCount: report.SessionCount, ParserVersion: claudecode.ParserVersion,
		},
	); err != nil {
		return report, err
	}
	return report, nil
}

func (s *claudeScanner) plan(
	ctx context.Context,
	files []claudecode.File,
	previous []domain.SourceFileState,
	lastFull *time.Time,
	forceFull bool,
	now time.Time,
) (string, []claudeFileTask, map[string]domain.SourceFileState, error) {
	previousByPath := make(map[string]domain.SourceFileState, len(previous))
	states := make(map[string]domain.SourceFileState, len(previous))
	for _, file := range previous {
		previousByPath[file.Path] = file
		states[file.Path] = file
	}
	metadata := make(map[string]claudecode.FileMetadata, len(files))
	for _, file := range files {
		select {
		case <-ctx.Done():
			return "", nil, nil, ctx.Err()
		default:
		}
		value, err := claudecode.Inspect(file)
		if err != nil {
			return "", nil, nil, safeClaudeError("读取 transcript 状态", err)
		}
		metadata[claudecode.CheckpointKey(file)] = value
	}
	full := forceFull || lastFull == nil || now.Sub(*lastFull) >= fullVerificationInterval
	for _, old := range previous {
		if _, exists := metadata[old.Path]; !exists {
			full = true
			break
		}
	}
	if full {
		return s.fullPlan(files, metadata)
	}
	tasks := make([]claudeFileTask, 0)
	for _, file := range files {
		checkpointKey := claudecode.CheckpointKey(file)
		current := metadata[checkpointKey]
		old, exists := previousByPath[checkpointKey]
		if !exists {
			tasks = append(tasks, claudeFileTask{file: file, metadata: current})
			continue
		}
		if old.ParserVersion != claudecode.ParserVersion {
			return s.fullPlan(files, metadata)
		}
		if sameClaudeFileState(old, current) {
			continue
		}
		if current.Size <= old.SizeBytes {
			return s.fullPlan(files, metadata)
		}
		appendSafe, err := claudecode.MatchesAppendPrefix(file, claudecode.FileMetadata{
			Identity: old.FileIdentity, Size: old.SizeBytes, MtimeNS: old.MtimeNS,
			HeadHash: old.HeadHash, TailHash: old.TailHash,
		})
		if err != nil {
			return "", nil, nil, safeClaudeError("校验 transcript 追加", err)
		}
		if !appendSafe {
			return s.fullPlan(files, metadata)
		}
		var parserState claudecode.ParserState
		if err := json.Unmarshal([]byte(old.ParserStateJSON), &parserState); err != nil {
			return s.fullPlan(files, metadata)
		}
		tasks = append(tasks, claudeFileTask{
			file: file, metadata: current, offset: old.CompleteLineEnd, state: parserState,
		})
	}
	return "incremental", tasks, states, nil
}

func (s *claudeScanner) fullPlan(files []claudecode.File, metadata map[string]claudecode.FileMetadata) (
	string, []claudeFileTask, map[string]domain.SourceFileState, error,
) {
	tasks := make([]claudeFileTask, 0, len(files))
	for _, file := range files {
		tasks = append(tasks, claudeFileTask{file: file, metadata: metadata[claudecode.CheckpointKey(file)]})
	}
	return "full", tasks, make(map[string]domain.SourceFileState, len(files)), nil
}

func sameClaudeFileState(previous domain.SourceFileState, current claudecode.FileMetadata) bool {
	return previous.FileIdentity == current.Identity && previous.SizeBytes == current.Size &&
		previous.MtimeNS == current.MtimeNS && previous.HeadHash == current.HeadHash &&
		previous.TailHash == current.TailHash
}

func safeClaudeError(operation string, err error) error {
	switch {
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("Claude Code %s失败: 权限不足", operation)
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("Claude Code %s失败: 文件在扫描期间被移动", operation)
	case operation == "解析 transcript":
		return fmt.Errorf("Claude Code %s失败: %w", operation, err)
	default:
		return fmt.Errorf("Claude Code %s失败", operation)
	}
}
