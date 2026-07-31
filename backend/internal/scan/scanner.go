package scan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	EventsSeen   int
	EventsStored int
	Warnings     []string
	FinishedAt   time.Time
}

type Scanner struct {
	store  *dorasqlite.Store
	homes  []string
	parser codex.Parser
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
	startedAt := s.now().UTC()
	runID, err := newRunID()
	if err != nil {
		return Report{}, err
	}
	report.RunID = runID

	report.Mode = "planning"
	if err := s.store.BeginUsageScan(ctx, runID, report.Mode, startedAt); err != nil {
		return Report{}, err
	}
	defer func() {
		if returnErr == nil {
			return
		}
		finishedAt := s.now().UTC()
		if err := s.store.FailUsageScan(
			context.Background(),
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
		return report, err
	}
	files, err := codex.Discover(homes)
	if err != nil {
		return report, err
	}
	report.FilesSeen = len(files)

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
			return report, fmt.Errorf("解析 Codex 文件 %q: %w", task.file.Path, err)
		}
		valid, err := codex.MatchesSnapshot(task.file, task.metadata)
		if err != nil {
			return report, fmt.Errorf("校验 Codex 文件快照 %q: %w", task.file.Path, err)
		}
		if !valid {
			return report, fmt.Errorf("Codex 文件 %q 在扫描期间发生非追加变化", task.file.Path)
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
	if err := s.store.CompleteUsageScan(
		ctx,
		runID,
		report.FinishedAt,
		events,
		orderedStates,
		report.FilesSeen,
		report.EventsSeen,
		strings.Join(report.Warnings, "；"),
	); err != nil {
		return report, err
	}
	return report, nil
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
			return "", nil, nil, nil, err
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
			return "", nil, nil, nil, err
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
