package scan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/provider/codex"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

const fullVerificationInterval = 24 * time.Hour

type Report struct {
	RunID        string
	Mode         string
	FilesSeen    int
	SessionCount int
	EventsSeen   int
	EventsStored int
	Warnings     []string
	FinishedAt   time.Time
	Providers    []ProviderReport
}

type ProviderReport struct {
	Source       string
	RunID        string
	Mode         string
	FilesSeen    int
	SessionCount int
	EventsSeen   int
	EventsStored int
	Warnings     []string
	FinishedAt   time.Time
	Error        string
}

type Scanner struct {
	store  *dorasqlite.Store
	homes  []string
	parser codex.Parser
	claude *claudeScanner
	now    func() time.Time
	// beforeRun 仅用于让并发测试稳定地控制扫描起点。
	beforeRun func()
	// beforeParse 仅用于测试扫描计划与文件读取之间的变化。
	beforeParse func(codex.File)

	mu      sync.Mutex
	current *scanCall
}

type scanCall struct {
	done   chan struct{}
	report Report
	err    error
}

type fileTask struct {
	file     codex.File
	metadata codex.FileMetadata
	offset   int64
	state    codex.ParserState
}

func New(store *dorasqlite.Store, homes []string) *Scanner {
	return &Scanner{
		store:  store,
		homes:  append([]string(nil), homes...),
		parser: codex.NewParser(),
		now:    time.Now,
	}
}

func NewWithClaude(store *dorasqlite.Store, codexHomes, claudeHomes []string) *Scanner {
	scanner := New(store, codexHomes)
	scanner.claude = newClaudeScanner(store, claudeHomes)
	return scanner
}

func (s *Scanner) Scan(ctx context.Context, forceFull bool) (Report, error) {
	s.mu.Lock()
	if s.current != nil {
		call := s.current
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return Report{}, ctx.Err()
		case <-call.done:
			if call.err != nil || !forceFull || call.report.Mode == "full" {
				return call.report, call.err
			}
			return s.Scan(ctx, true)
		}
	}
	call := &scanCall{done: make(chan struct{})}
	s.current = call
	s.mu.Unlock()

	if s.beforeRun != nil {
		s.beforeRun()
	}
	call.report, call.err = s.scan(ctx, forceFull)
	s.mu.Lock()
	s.current = nil
	close(call.done)
	s.mu.Unlock()
	return call.report, call.err
}

func (s *Scanner) scan(ctx context.Context, forceFull bool) (report Report, returnErr error) {
	codexReport, codexErr := s.scanCodex(ctx, forceFull)
	if s.claude == nil {
		codexReport.Providers = []ProviderReport{providerReport(domain.CodexSource, codexReport, codexErr)}
		return codexReport, codexErr
	}
	claudeReport, claudeErr := s.claude.scan(ctx, forceFull)
	report = Report{
		RunID:        codexReport.RunID,
		Mode:         combinedMode(forceFull, codexReport.Mode, claudeReport.Mode),
		FilesSeen:    codexReport.FilesSeen + claudeReport.FilesSeen,
		SessionCount: codexReport.SessionCount + claudeReport.SessionCount,
		EventsSeen:   codexReport.EventsSeen + claudeReport.EventsSeen,
		EventsStored: codexReport.EventsStored + claudeReport.EventsStored,
		FinishedAt:   latestTime(codexReport.FinishedAt, claudeReport.FinishedAt),
		Providers: []ProviderReport{
			providerReport(domain.CodexSource, codexReport, codexErr),
			providerReport(domain.ClaudeCodeSource, claudeReport, claudeErr),
		},
	}
	for _, warning := range codexReport.Warnings {
		report.Warnings = append(report.Warnings, "Codex: "+warning)
	}
	for _, warning := range claudeReport.Warnings {
		report.Warnings = append(report.Warnings, "Claude Code: "+warning)
	}
	if codexErr != nil {
		return report, errors.Join(fmt.Errorf("%s 扫描失败: %w", domain.CodexSource, codexErr), providerError(domain.ClaudeCodeSource, claudeErr))
	}
	return report, providerError(domain.ClaudeCodeSource, claudeErr)
}

func providerError(source string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s 扫描失败: %w", source, err)
}

func providerReport(source string, report Report, err error) ProviderReport {
	result := ProviderReport{
		Source: source, RunID: report.RunID, Mode: report.Mode,
		FilesSeen: report.FilesSeen, SessionCount: report.SessionCount,
		EventsSeen: report.EventsSeen, EventsStored: report.EventsStored,
		Warnings: append([]string(nil), report.Warnings...), FinishedAt: report.FinishedAt,
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func combinedMode(forceFull bool, modes ...string) string {
	if forceFull {
		return "full"
	}
	if len(modes) == 0 {
		return "incremental"
	}
	for _, mode := range modes[1:] {
		if mode != modes[0] {
			return "mixed"
		}
	}
	return modes[0]
}

func latestTime(values ...time.Time) time.Time {
	var result time.Time
	for _, value := range values {
		if value.After(result) {
			result = value
		}
	}
	return result
}

func (s *Scanner) scanCodex(ctx context.Context, forceFull bool) (report Report, returnErr error) {
	startedAt := s.now().UTC()
	runID, err := newRunID()
	if err != nil {
		return Report{}, err
	}
	report.RunID = runID

	report.Mode = "planning"
	if err := s.store.BeginProviderUsageScan(ctx, domain.CodexSource, runID, report.Mode, startedAt); err != nil {
		return Report{}, err
	}
	defer func() {
		if returnErr == nil {
			return
		}
		finishedAt := s.now().UTC()
		if err := s.store.FailProviderUsageScan(
			context.Background(),
			domain.CodexSource,
			runID,
			finishedAt,
			report.FilesSeen,
			report.EventsSeen,
			returnErr,
		); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()

	homes, err := codex.ResolveHomes(s.homes)
	if err != nil {
		return report, safeCodexError("解析数据目录", err)
	}
	files, err := codex.Discover(homes)
	if err != nil {
		return report, safeCodexError("发现 transcript", err)
	}
	report.FilesSeen = len(files)
	report.SessionCount = len(files)
	configFound := directoryFound(homes)

	previousFiles, err := s.store.LoadSourceFiles(ctx, domain.CodexSource)
	if err != nil {
		return report, err
	}
	lastFull, err := s.store.LastSuccessfulFullScan(ctx, domain.CodexSource)
	if err != nil {
		return report, err
	}

	mode, tasks, _, states, err := s.plan(ctx, files, previousFiles, lastFull, forceFull, startedAt)
	if err != nil {
		return report, err
	}
	report.Mode = mode
	if err := s.store.UpdateUsageScanMode(ctx, runID, mode); err != nil {
		return report, err
	}

	events := make([]domain.UsageEvent, 0)
	if mode != "full" {
		events, err = s.store.LoadUsageEvents(ctx, domain.CodexSource)
		if err != nil {
			return Report{}, err
		}
	}

	warnings := make([]string, 0)
	for _, task := range tasks {
		if s.beforeParse != nil {
			s.beforeParse(task.file)
		}
		result, err := s.parser.ParseFileSnapshot(
			ctx,
			task.file,
			task.offset,
			task.metadata.Size,
			task.state,
		)
		if err != nil {
			return report, safeCodexError("解析 transcript", err)
		}
		valid, err := codex.MatchesSnapshot(task.file, task.metadata)
		if err != nil {
			return report, safeCodexError("校验 transcript 快照", err)
		}
		if !valid {
			return report, errors.New("Codex transcript 在扫描期间发生非追加变化")
		}
		report.EventsSeen += len(result.Events)
		events = append(events, result.Events...)
		warnings = append(warnings, result.Warnings...)

		stateJSON, err := json.Marshal(result.State)
		if err != nil {
			return report, fmt.Errorf("保存 Codex parser 状态: %w", err)
		}
		parsedOffset := result.CompleteLineEnd
		completeLineEnd := result.CompleteLineEnd
		if task.file.Compressed {
			parsedOffset = task.metadata.Size
			completeLineEnd = task.metadata.Size
		}
		states[task.file.Path] = domain.SourceFileState{
			Source:          domain.CodexSource,
			Path:            task.file.Path,
			FileIdentity:    task.metadata.Identity,
			SizeBytes:       task.metadata.Size,
			MtimeNS:         task.metadata.MtimeNS,
			ParsedOffset:    parsedOffset,
			CompleteLineEnd: completeLineEnd,
			HeadHash:        task.metadata.HeadHash,
			TailHash:        task.metadata.TailHash,
			ParserVersion:   codex.ParserVersion,
			ParserStateJSON: string(stateJSON),
			LastSuccessAt:   startedAt,
		}
	}

	events = codex.Reconcile(events)
	report.EventsStored = len(events)
	report.Warnings = uniqueStrings(warnings)
	report.FinishedAt = s.now().UTC()

	orderedStates := make([]domain.SourceFileState, 0, len(states))
	for _, file := range files {
		orderedStates = append(orderedStates, states[file.Path])
	}
	status := "ready"
	if !configFound {
		status = "not_found"
	} else if len(report.Warnings) > 0 {
		status = "degraded"
	}
	if err := s.store.CompleteProviderUsageScanWithMetrics(
		ctx,
		domain.CodexSource,
		runID,
		report.FinishedAt,
		events,
		orderedStates,
		report.FilesSeen,
		report.EventsSeen,
		strings.Join(report.Warnings, "；"),
		domain.UsageScanMetrics{
			Status:        status,
			ConfigFound:   configFound,
			SessionCount:  len(files),
			ParserVersion: codex.ParserVersion,
		},
	); err != nil {
		return report, err
	}
	return report, nil
}

func directoryFound(paths []string) bool {
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

func safeCodexError(operation string, err error) error {
	switch {
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("Codex %s失败: 权限不足", operation)
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("Codex %s失败: 文件在扫描期间被移动", operation)
	case strings.Contains(err.Error(), string(os.PathSeparator)):
		return fmt.Errorf("Codex %s失败: 本地文件错误（路径已隐藏）", operation)
	default:
		return fmt.Errorf("Codex %s失败: %w", operation, err)
	}
}

func (s *Scanner) plan(
	ctx context.Context,
	files []codex.File,
	previous []domain.SourceFileState,
	lastFull *time.Time,
	forceFull bool,
	now time.Time,
) (
	string,
	[]fileTask,
	map[string]codex.FileMetadata,
	map[string]domain.SourceFileState,
	error,
) {
	previousByPath := make(map[string]domain.SourceFileState, len(previous))
	states := make(map[string]domain.SourceFileState, len(previous))
	for _, file := range previous {
		previousByPath[file.Path] = file
		states[file.Path] = file
	}

	metadata := make(map[string]codex.FileMetadata, len(files))
	for _, file := range files {
		select {
		case <-ctx.Done():
			return "", nil, nil, nil, ctx.Err()
		default:
		}
		value, err := codex.Inspect(file)
		if err != nil {
			return "", nil, nil, nil, safeCodexError("读取 transcript 状态", err)
		}
		metadata[file.Path] = value
	}

	full := forceFull || lastFull == nil || now.Sub(*lastFull) >= fullVerificationInterval
	for _, old := range previous {
		if _, exists := metadata[old.Path]; !exists {
			full = true
			break
		}
	}
	if full {
		tasks := make([]fileTask, 0, len(files))
		for _, file := range files {
			tasks = append(tasks, fileTask{file: file, metadata: metadata[file.Path]})
		}
		return "full", tasks, metadata, make(map[string]domain.SourceFileState, len(files)), nil
	}

	tasks := make([]fileTask, 0)
	for _, file := range files {
		current := metadata[file.Path]
		old, exists := previousByPath[file.Path]
		if !exists {
			if file.Compressed {
				return s.fullPlan(files, metadata)
			}
			tasks = append(tasks, fileTask{file: file, metadata: current})
			continue
		}

		if old.ParserVersion != codex.ParserVersion {
			return s.fullPlan(files, metadata)
		}
		if sameFileState(old, current) {
			continue
		}
		if file.Compressed || current.Size <= old.SizeBytes || old.ParsedOffset != old.CompleteLineEnd {
			return s.fullPlan(files, metadata)
		}

		appendSafe, err := codex.MatchesAppendPrefix(file, codex.FileMetadata{
			Identity: old.FileIdentity,
			Size:     old.SizeBytes,
			MtimeNS:  old.MtimeNS,
			HeadHash: old.HeadHash,
			TailHash: old.TailHash,
		})
		if err != nil {
			return "", nil, nil, nil, safeCodexError("校验 transcript 追加", err)
		}
		if !appendSafe {
			return s.fullPlan(files, metadata)
		}

		var parserState codex.ParserState
		if err := json.Unmarshal([]byte(old.ParserStateJSON), &parserState); err != nil {
			return s.fullPlan(files, metadata)
		}
		// 首个 turn_context 可能要求回填早期模型，此时重建文件更安全。
		if !parserState.SawTurnContext {
			return s.fullPlan(files, metadata)
		}
		tasks = append(tasks, fileTask{
			file:     file,
			metadata: current,
			offset:   old.CompleteLineEnd,
			state:    parserState,
		})
	}
	return "incremental", tasks, metadata, states, nil
}

func (s *Scanner) fullPlan(files []codex.File, metadata map[string]codex.FileMetadata) (
	string,
	[]fileTask,
	map[string]codex.FileMetadata,
	map[string]domain.SourceFileState,
	error,
) {
	tasks := make([]fileTask, 0, len(files))
	for _, file := range files {
		tasks = append(tasks, fileTask{file: file, metadata: metadata[file.Path]})
	}
	return "full", tasks, metadata, make(map[string]domain.SourceFileState, len(files)), nil
}

func sameFileState(previous domain.SourceFileState, current codex.FileMetadata) bool {
	return previous.FileIdentity == current.Identity &&
		previous.SizeBytes == current.Size &&
		previous.MtimeNS == current.MtimeNS &&
		previous.HeadHash == current.HeadHash &&
		previous.TailHash == current.TailHash
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func newRunID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("生成扫描 ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
