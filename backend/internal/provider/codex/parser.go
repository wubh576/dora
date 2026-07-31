package codex

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/wubh576/dora/backend/internal/domain"
)

const (
	ParserVersion       = 2
	defaultMaxLineBytes = 8 * 1024 * 1024
	linePrefixBytes     = 64 * 1024
)

var recordTypePattern = regexp.MustCompile(`"type"\s*:\s*"([^"]+)"`)

type ParserState struct {
	HomeKey               string           `json:"homeKey"`
	RolloutKey            string           `json:"rolloutKey"`
	ParentRolloutKey      string           `json:"parentRolloutKey"`
	Project               string           `json:"project"`
	Model                 string           `json:"model"`
	SawTurnContext        bool             `json:"sawTurnContext"`
	LineageAmbiguous      bool             `json:"lineageAmbiguous"`
	CumulativeRaw         *normalizedUsage `json:"cumulativeRaw,omitempty"`
	CumulativeRawComplete bool             `json:"cumulativeRawComplete,omitempty"`
	CumulativeTotal       *normalizedUsage `json:"cumulativeTotal,omitempty"`
	CumulativeComplete    bool             `json:"cumulativeComplete,omitempty"`
	CumulativePending     *normalizedUsage `json:"cumulativePending,omitempty"`
	PendingComplete       bool             `json:"pendingComplete,omitempty"`
	CumulativePendingKeys map[string]bool  `json:"cumulativePendingKeys,omitempty"`
}

type ParseResult struct {
	Events          []domain.UsageEvent
	State           ParserState
	CompleteLineEnd int64
	LinesSeen       int
	Warnings        []string
}

type Parser struct {
	MaxLineBytes int
}

func NewParser() Parser {
	return Parser{MaxLineBytes: defaultMaxLineBytes}
}

func (p Parser) ParseFile(ctx context.Context, file File, offset int64, state ParserState) (ParseResult, error) {
	return p.parseFile(ctx, file, offset, -1, state)
}

func (p Parser) ParseFileSnapshot(
	ctx context.Context,
	file File,
	offset int64,
	snapshotEnd int64,
	state ParserState,
) (ParseResult, error) {
	if snapshotEnd < offset {
		return ParseResult{}, errors.New("Codex parser snapshot 范围无效")
	}
	return p.parseFile(ctx, file, offset, snapshotEnd, state)
}

func (p Parser) parseFile(
	ctx context.Context,
	file File,
	offset int64,
	snapshotEnd int64,
	state ParserState,
) (ParseResult, error) {
	if p.MaxLineBytes <= 0 {
		p.MaxLineBytes = defaultMaxLineBytes
	}
	if offset < 0 || file.Compressed && offset != 0 {
		return ParseResult{}, errors.New("Codex parser offset 无效")
	}

	reader, closeReader, err := openFile(file, offset)
	if err != nil {
		return ParseResult{}, err
	}
	defer closeReader()
	if snapshotEnd >= 0 && !file.Compressed {
		reader = io.LimitReader(reader, snapshotEnd-offset)
	}

	if state.HomeKey == "" {
		state.HomeKey = file.HomeKey
	}
	if state.Project == "" {
		state.Project = "unknown"
	}
	if state.Model == "" {
		state.Model = "unknown"
	}

	result := ParseResult{State: state, CompleteLineEnd: offset}
	parsedEvents := make([]parsedEvent, 0)
	buffered := bufio.NewReaderSize(reader, 64*1024)
	position := offset
	firstKnownModel := ""

	for {
		select {
		case <-ctx.Done():
			return ParseResult{}, ctx.Err()
		default:
		}

		line, consumed, complete, oversized, err := readBoundedLine(buffered, p.MaxLineBytes)
		if err != nil {
			return ParseResult{}, fmt.Errorf("读取 Codex JSONL: %w", err)
		}
		if consumed == 0 {
			break
		}
		position += consumed
		if !complete {
			break
		}

		result.CompleteLineEnd = position
		result.LinesSeen++
		if oversized {
			if safeOversizedRecord(line) {
				result.Warnings = append(result.Warnings, "已跳过不含 token usage 的超大 Codex 记录")
				continue
			}
			return ParseResult{}, fmt.Errorf("Codex JSONL 存在超过 %d 字节的未知记录", p.MaxLineBytes)
		}

		event, emitted, warnings, err := parseRecord(line, &result.State)
		if err != nil {
			return ParseResult{}, fmt.Errorf("解析 Codex JSONL 第 %d 行: %w", result.LinesSeen, err)
		}
		if firstKnownModel == "" && result.State.Model != "" && result.State.Model != "unknown" {
			firstKnownModel = result.State.Model
		}
		result.Warnings = append(result.Warnings, warnings...)
		if emitted {
			parsedEvents = append(parsedEvents, event)
		}
	}
	if snapshotEnd >= 0 && !file.Compressed && position != snapshotEnd {
		return ParseResult{}, errors.New("Codex 数据文件在扫描期间变短")
	}

	// 无 lineage 的早期事件可以使用本文件后来出现的确定模型。
	if !state.SawTurnContext && firstKnownModel != "" && result.State.ParentRolloutKey == "" && !result.State.LineageAmbiguous {
		for index := range parsedEvents {
			if parsedEvents[index].event.Model == "unknown" {
				parsedEvents[index].event.Model = firstKnownModel
			}
		}
	}

	result.Events = make([]domain.UsageEvent, 0, len(parsedEvents))
	for _, parsed := range parsedEvents {
		parsed.finalize(file.HomeKey)
		result.Events = append(result.Events, parsed.event)
	}
	return result, nil
}

type envelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMetaPayload struct {
	ID             string          `json:"id"`
	CWD            string          `json:"cwd"`
	ParentThreadID string          `json:"parent_thread_id"`
	ForkedFromID   string          `json:"forked_from_id"`
	Source         json.RawMessage `json:"source"`
}

type turnContextPayload struct {
	Model string `json:"model"`
}

type eventMessagePayload struct {
	Type string     `json:"type"`
	Info *tokenInfo `json:"info"`
}

type tokenInfo struct {
	Last  *rawTokenUsage `json:"last_token_usage"`
	Total *rawTokenUsage `json:"total_token_usage"`
	Model string         `json:"model"`
}

type rawTokenUsage struct {
	Input         *int64 `json:"input_tokens"`
	Output        *int64 `json:"output_tokens"`
	Cached        *int64 `json:"cached_input_tokens"`
	CacheRead     *int64 `json:"cache_read_input_tokens"`
	CacheCreation *int64 `json:"cache_creation_input_tokens"`
	CacheWrite    *int64 `json:"cache_write_input_tokens"`
	Reasoning     *int64 `json:"reasoning_output_tokens"`
	Total         *int64 `json:"total_tokens"`
}

type normalizedUsage struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	Cached        int64 `json:"cached"`
	CacheCreation int64 `json:"cacheCreation"`
	Reasoning     int64 `json:"reasoning"`
	ReportedTotal int64 `json:"reportedTotal"`
	Total         int64 `json:"total"`
}

type parsedEvent struct {
	event         domain.UsageEvent
	last          normalizedUsage
	total         normalizedUsage
	totalComplete bool
}

func (p *parsedEvent) finalize(homeKey string) {
	p.event.DedupKey = usageKey(homeKey, p.event.Model, p.last, p.total)
	if p.totalComplete {
		p.event.ReplayFingerprint = replayKey(homeKey, p.last, p.total)
	}
}

func parseRecord(line []byte, state *ParserState) (parsedEvent, bool, []string, error) {
	var record envelope
	if err := json.Unmarshal(line, &record); err != nil {
		return parsedEvent{}, false, nil, err
	}

	switch record.Type {
	case "session_meta":
		warnings, err := applySessionMeta(record.Payload, state)
		return parsedEvent{}, false, warnings, err
	case "turn_context":
		var payload turnContextPayload
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			return parsedEvent{}, false, nil, err
		}
		state.SawTurnContext = true
		if strings.TrimSpace(payload.Model) != "" {
			state.Model = payload.Model
		}
		return parsedEvent{}, false, nil, nil
	case "event_msg":
		return parseTokenEvent(record, state)
	default:
		return parsedEvent{}, false, nil, nil
	}
}

func applySessionMeta(raw json.RawMessage, state *ParserState) ([]string, error) {
	var payload sessionMetaPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload.ID != "" {
		nextRolloutKey := rolloutKey(state.HomeKey, payload.ID)
		if state.RolloutKey != "" && state.RolloutKey != nextRolloutKey {
			state.CumulativeRaw = nil
			state.CumulativeRawComplete = false
			state.CumulativeTotal = nil
			state.CumulativeComplete = false
			state.CumulativePending = nil
			state.PendingComplete = false
			state.CumulativePendingKeys = nil
		}
		state.RolloutKey = nextRolloutKey
	}
	if payload.CWD != "" {
		project := filepath.Base(filepath.Clean(payload.CWD))
		if project != "." && project != string(filepath.Separator) {
			state.Project = project
		}
	}

	parentIDs := make(map[string]struct{})
	for _, value := range []string{payload.ParentThreadID, payload.ForkedFromID, nestedParentID(payload.Source)} {
		if value != "" {
			parentIDs[value] = struct{}{}
		}
	}
	switch len(parentIDs) {
	case 0:
		state.ParentRolloutKey = ""
	case 1:
		for parentID := range parentIDs {
			state.ParentRolloutKey = rolloutKey(state.HomeKey, parentID)
		}
	default:
		state.ParentRolloutKey = ""
		state.LineageAmbiguous = true
		return []string{"Codex lineage 存在冲突，已保留相关 usage"}, nil
	}
	return nil, nil
}

func nestedParentID(raw json.RawMessage) string {
	if len(raw) == 0 || raw[0] != '{' {
		return ""
	}
	var source struct {
		Subagent struct {
			ThreadSpawn struct {
				ParentThreadID string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	if json.Unmarshal(raw, &source) != nil {
		return ""
	}
	return source.Subagent.ThreadSpawn.ParentThreadID
}

func parseTokenEvent(record envelope, state *ParserState) (parsedEvent, bool, []string, error) {
	var payload eventMessagePayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return parsedEvent{}, false, nil, err
	}
	if payload.Type != "token_count" || payload.Info == nil {
		return parsedEvent{}, false, nil, nil
	}

	occurredAt, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil {
		return parsedEvent{}, false, nil, errors.New("token_count 时间戳无效")
	}

	if payload.Info.Last == nil && payload.Info.Total == nil {
		return parsedEvent{}, false, nil, nil
	}

	model := payload.Info.Model
	if strings.TrimSpace(model) == "" {
		model = state.Model
	}
	if model == "" {
		model = "unknown"
	}
	project := state.Project
	if project == "" {
		project = "unknown"
	}
	inherited := !state.SawTurnContext && state.ParentRolloutKey != "" && !state.LineageAmbiguous

	var last normalizedUsage
	var lastComplete bool
	lastKey := ""
	lastWasPending := false
	if payload.Info.Last != nil {
		last, lastComplete, err = normalizeUsage(payload.Info.Last)
		if err != nil {
			return parsedEvent{}, false, nil, err
		}
		lastKey = usageKey(state.HomeKey, model, last, normalizedUsage{})
		lastWasPending = state.CumulativePendingKeys[lastKey]
	}

	var total normalizedUsage
	var totalComplete bool
	var fallback normalizedUsage
	var warnings []string
	if payload.Info.Total != nil {
		total, totalComplete, err = normalizeUsage(payload.Info.Total)
		if err != nil {
			return parsedEvent{}, false, nil, err
		}
		fallback, total, totalComplete, warnings, err = state.advanceCumulative(total, totalComplete)
		if err != nil {
			return parsedEvent{}, false, nil, err
		}
	}

	switch {
	case payload.Info.Last != nil && payload.Info.Total == nil:
		added, err := state.observeWithoutCumulative(last, lastComplete, lastKey)
		if err != nil {
			return parsedEvent{}, false, nil, err
		}
		if !added {
			return parsedEvent{}, false, nil, nil
		}
	case payload.Info.Last != nil && lastWasPending:
		last = fallback
		if last.Total == 0 {
			return parsedEvent{}, false, warnings, nil
		}
	case payload.Info.Last == nil:
		last = fallback
		if last.Total == 0 {
			return parsedEvent{}, false, warnings, nil
		}
	}

	return parsedEvent{
		event: domain.UsageEvent{
			Source:                   domain.CodexSource,
			OccurredAt:               occurredAt.UTC(),
			Model:                    model,
			Project:                  project,
			InputTokens:              last.Input,
			OutputTokens:             last.Output,
			CachedInputTokens:        last.Cached,
			CacheCreationInputTokens: last.CacheCreation,
			ReasoningOutputTokens:    last.Reasoning,
			ReportedTotalTokens:      last.ReportedTotal,
			TotalTokens:              last.Total,
			RolloutKey:               state.RolloutKey,
			ParentRolloutKey:         state.ParentRolloutKey,
			InheritedReplay:          inherited,
		},
		last:          last,
		total:         total,
		totalComplete: totalComplete,
	}, true, warnings, nil
}

func (s *ParserState) advanceCumulative(
	current normalizedUsage,
	currentComplete bool,
) (normalizedUsage, normalizedUsage, bool, []string, error) {
	previousRaw := s.CumulativeRaw
	previousTotal := s.CumulativeTotal
	hadTotal := previousTotal != nil
	if previousTotal == nil {
		previousTotal = cloneUsage(normalizedUsage{})
	}

	delta := current
	comparable := currentComplete
	reset := false
	if previousRaw != nil {
		var err error
		delta, comparable, reset, err = cumulativeDelta(
			*previousRaw,
			current,
			s.CumulativeRawComplete,
			currentComplete,
		)
		if err != nil {
			return normalizedUsage{}, normalizedUsage{}, false, nil, err
		}
	}
	reconciled := true
	var pendingWarning string
	if reset {
		s.CumulativePending = nil
		s.PendingComplete = false
		s.CumulativePendingKeys = nil
	} else if s.CumulativePending != nil && s.CumulativePending.Total > 0 {
		var remainingPending *normalizedUsage
		delta, remainingPending, reconciled = reconcilePending(
			delta,
			*s.CumulativePending,
			comparable,
			s.PendingComplete,
		)
		s.CumulativePending = remainingPending
		s.PendingComplete = s.PendingComplete && reconciled
		if remainingPending == nil {
			s.PendingComplete = false
			s.CumulativePendingKeys = nil
		}
		if !reconciled {
			pendingWarning = "Codex 累计 token 与缺少 total 的事件明细不可直接对账，仅保留 total"
		}
	}

	accumulated, err := addUsage(*previousTotal, delta)
	if err != nil {
		return normalizedUsage{}, normalizedUsage{}, false, nil, err
	}
	complete := currentComplete && comparable && reconciled
	if hadTotal {
		complete = s.CumulativeComplete && complete
	}
	s.CumulativeRaw = cloneUsage(current)
	s.CumulativeRawComplete = currentComplete
	s.CumulativeTotal = cloneUsage(accumulated)
	s.CumulativeComplete = complete

	var warnings []string
	switch {
	case reset:
		warnings = append(warnings, "Codex 累计 token 计数器已重置，已从新基线继续计数")
	case !comparable && delta.Total > 0:
		warnings = append(warnings, "Codex 累计 token 明细不可直接比较，仅保留 total 增量")
	}
	if pendingWarning != "" {
		warnings = append(warnings, pendingWarning)
	}
	return delta, accumulated, complete, warnings, nil
}

func (s *ParserState) observeWithoutCumulative(
	last normalizedUsage,
	lastComplete bool,
	key string,
) (bool, error) {
	if s.CumulativePendingKeys[key] {
		return false, nil
	}
	if s.CumulativePendingKeys == nil {
		s.CumulativePendingKeys = make(map[string]bool)
	}
	s.CumulativePendingKeys[key] = true
	hadPending := s.CumulativePending != nil
	if !hadPending {
		s.CumulativePending = cloneUsage(normalizedUsage{})
	}
	pending, err := addUsage(*s.CumulativePending, last)
	if err != nil {
		return false, err
	}
	s.CumulativePending = cloneUsage(pending)
	if hadPending {
		s.PendingComplete = s.PendingComplete && lastComplete
	} else {
		s.PendingComplete = lastComplete
	}

	hadTotal := s.CumulativeTotal != nil
	if !hadTotal {
		s.CumulativeTotal = cloneUsage(normalizedUsage{})
	}
	accumulated, err := addUsage(*s.CumulativeTotal, last)
	if err != nil {
		return false, err
	}
	s.CumulativeTotal = cloneUsage(accumulated)
	if hadTotal {
		s.CumulativeComplete = s.CumulativeComplete && lastComplete
	} else {
		s.CumulativeComplete = lastComplete
	}
	return true, nil
}

func reconcilePending(
	increment normalizedUsage,
	pending normalizedUsage,
	incrementComplete bool,
	pendingComplete bool,
) (normalizedUsage, *normalizedUsage, bool) {
	if increment.Total >= pending.Total {
		if incrementComplete && pendingComplete {
			if remaining, ok := subtractUsage(increment, pending); ok {
				return remaining, nil, true
			}
		}
		return totalOnlyDifference(increment, pending.Total), nil, false
	}

	remainingTotal := pending.Total - increment.Total
	if incrementComplete && pendingComplete {
		if remaining, ok := subtractUsage(pending, increment); ok {
			return normalizedUsage{}, cloneUsage(remaining), true
		}
	}
	return normalizedUsage{}, cloneUsage(normalizedUsage{
		ReportedTotal: remainingTotal,
		Total:         remainingTotal,
	}), false
}

func subtractUsage(larger, smaller normalizedUsage) (normalizedUsage, bool) {
	if larger.Total < smaller.Total || !sameOrIncreasingDetails(smaller, larger) {
		return normalizedUsage{}, false
	}
	result := normalizedUsage{
		Input:         larger.Input - smaller.Input,
		Output:        larger.Output - smaller.Output,
		Cached:        larger.Cached - smaller.Cached,
		CacheCreation: larger.CacheCreation - smaller.CacheCreation,
		Reasoning:     larger.Reasoning - smaller.Reasoning,
		ReportedTotal: larger.ReportedTotal - minToken(larger.ReportedTotal, smaller.ReportedTotal),
		Total:         larger.Total - smaller.Total,
	}
	detail, err := sumTokens(result.Input, result.Output, result.Cached, result.CacheCreation, result.Reasoning)
	if err != nil || detail > result.Total {
		return normalizedUsage{}, false
	}
	result.ReportedTotal = minToken(result.ReportedTotal, result.Total)
	return result, true
}

func totalOnlyDifference(value normalizedUsage, covered int64) normalizedUsage {
	remaining := value.Total - covered
	if remaining == 0 {
		return normalizedUsage{}
	}
	reported := value.ReportedTotal - minToken(value.ReportedTotal, covered)
	return normalizedUsage{ReportedTotal: minToken(reported, remaining), Total: remaining}
}

func cumulativeDelta(
	previous normalizedUsage,
	current normalizedUsage,
	previousComplete bool,
	currentComplete bool,
) (normalizedUsage, bool, bool, error) {
	if current.Total < previous.Total {
		return current, currentComplete, true, nil
	}
	totalDelta := current.Total - previous.Total
	if totalDelta == 0 {
		comparable := previousComplete &&
			currentComplete &&
			sameOrIncreasingDetails(previous, current)
		return normalizedUsage{}, comparable, false, nil
	}

	comparable := previousComplete &&
		currentComplete &&
		sameOrIncreasingDetails(previous, current)
	reportedDelta := int64(0)
	if current.ReportedTotal >= previous.ReportedTotal {
		reportedDelta = minToken(current.ReportedTotal-previous.ReportedTotal, totalDelta)
	}
	delta := normalizedUsage{
		ReportedTotal: reportedDelta,
		Total:         totalDelta,
	}
	if !comparable {
		return delta, false, false, nil
	}
	delta.Input = current.Input - previous.Input
	delta.Output = current.Output - previous.Output
	delta.Cached = current.Cached - previous.Cached
	delta.CacheCreation = current.CacheCreation - previous.CacheCreation
	delta.Reasoning = current.Reasoning - previous.Reasoning
	detail, err := sumTokens(delta.Input, delta.Output, delta.Cached, delta.CacheCreation, delta.Reasoning)
	if err != nil {
		return normalizedUsage{}, false, false, err
	}
	if detail > totalDelta {
		return normalizedUsage{ReportedTotal: reportedDelta, Total: totalDelta}, false, false, nil
	}
	return delta, true, false, nil
}

func sameOrIncreasingDetails(previous, current normalizedUsage) bool {
	return current.Input >= previous.Input &&
		current.Output >= previous.Output &&
		current.Cached >= previous.Cached &&
		current.CacheCreation >= previous.CacheCreation &&
		current.Reasoning >= previous.Reasoning
}

func addUsage(left, right normalizedUsage) (normalizedUsage, error) {
	values := [7]int64{}
	for index, pair := range [][2]int64{
		{left.Input, right.Input},
		{left.Output, right.Output},
		{left.Cached, right.Cached},
		{left.CacheCreation, right.CacheCreation},
		{left.Reasoning, right.Reasoning},
		{left.ReportedTotal, right.ReportedTotal},
		{left.Total, right.Total},
	} {
		value, err := addTokenValues(pair[0], pair[1])
		if err != nil {
			return normalizedUsage{}, err
		}
		values[index] = value
	}
	return normalizedUsage{
		Input:         values[0],
		Output:        values[1],
		Cached:        values[2],
		CacheCreation: values[3],
		Reasoning:     values[4],
		ReportedTotal: values[5],
		Total:         values[6],
	}, nil
}

func cloneUsage(value normalizedUsage) *normalizedUsage {
	return &value
}

func normalizeUsage(raw *rawTokenUsage) (normalizedUsage, bool, error) {
	values := []*int64{raw.Input, raw.Output, raw.Cached, raw.CacheRead, raw.CacheCreation, raw.CacheWrite, raw.Reasoning, raw.Total}
	for _, value := range values {
		if value != nil && *value < 0 {
			return normalizedUsage{}, false, errors.New("token 字段不能为负数")
		}
	}

	inputRaw := valueOrZero(raw.Input)
	outputRaw := valueOrZero(raw.Output)
	cached, err := addTokenValues(valueOrZero(raw.Cached), valueOrZero(raw.CacheRead))
	if err != nil {
		return normalizedUsage{}, false, err
	}
	// 当前 Codex 使用 cache_write_input_tokens 表示 cache creation；标准字段存在时优先使用标准字段。
	cacheCreation := valueOrZero(raw.CacheWrite)
	if raw.CacheCreation != nil {
		cacheCreation = *raw.CacheCreation
	}
	reasoning := valueOrZero(raw.Reasoning)

	cached = minToken(cached, inputRaw)
	cacheCreation = minToken(cacheCreation, inputRaw-cached)
	reasoning = minToken(reasoning, outputRaw)
	input := inputRaw - cached - cacheCreation
	output := outputRaw - reasoning
	detail, err := sumTokens(input, output, cached, cacheCreation, reasoning)
	if err != nil {
		return normalizedUsage{}, false, err
	}
	reported := valueOrZero(raw.Total)
	total := reported
	if detail > total {
		total = detail
	}

	return normalizedUsage{
		Input:         input,
		Output:        output,
		Cached:        cached,
		CacheCreation: cacheCreation,
		Reasoning:     reasoning,
		ReportedTotal: reported,
		Total:         total,
	}, raw.Input != nil && raw.Output != nil && raw.Total != nil, nil
}

func addTokenValues(left, right int64) (int64, error) {
	if right > math.MaxInt64-left {
		return 0, errors.New("token 字段超出 int64")
	}
	return left + right, nil
}

func sumTokens(values ...int64) (int64, error) {
	var total int64
	for _, value := range values {
		next, err := addTokenValues(total, value)
		if err != nil {
			return 0, err
		}
		total = next
	}
	return total, nil
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func minToken(value, maximum int64) int64 {
	if value < maximum {
		return value
	}
	return maximum
}

func openFile(file File, offset int64) (io.Reader, func(), error) {
	handle, err := os.Open(file.Path)
	if err != nil {
		return nil, func() {}, fmt.Errorf("打开 Codex 数据文件: %w", err)
	}
	closeHandle := func() { _ = handle.Close() }

	switch fileExtension(file.Path) {
	case ".jsonl.gz":
		reader, err := gzip.NewReader(handle)
		if err != nil {
			closeHandle()
			return nil, func() {}, fmt.Errorf("打开 Codex gzip 数据: %w", err)
		}
		return reader, func() {
			_ = reader.Close()
			closeHandle()
		}, nil
	case ".jsonl.zst":
		reader, err := zstd.NewReader(handle)
		if err != nil {
			closeHandle()
			return nil, func() {}, fmt.Errorf("打开 Codex zstd 数据: %w", err)
		}
		return reader, func() {
			reader.Close()
			closeHandle()
		}, nil
	default:
		if _, err := handle.Seek(offset, io.SeekStart); err != nil {
			closeHandle()
			return nil, func() {}, fmt.Errorf("定位 Codex JSONL: %w", err)
		}
		return handle, closeHandle, nil
	}
}

func readBoundedLine(reader *bufio.Reader, maximum int) ([]byte, int64, bool, bool, error) {
	var line []byte
	var prefix []byte
	var consumed int64
	oversized := false

	for {
		fragment, err := reader.ReadSlice('\n')
		consumed += int64(len(fragment))
		if len(prefix) < linePrefixBytes {
			remaining := linePrefixBytes - len(prefix)
			if remaining > len(fragment) {
				remaining = len(fragment)
			}
			prefix = append(prefix, fragment[:remaining]...)
		}
		if !oversized {
			if len(line)+len(fragment) > maximum {
				oversized = true
				line = nil
			} else {
				line = append(line, fragment...)
			}
		}

		switch {
		case err == nil:
			if oversized {
				return prefix, consumed, true, true, nil
			}
			text := strings.TrimSuffix(string(line), "\n")
			text = strings.TrimSuffix(text, "\r")
			return []byte(text), consumed, true, false, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if consumed == 0 {
				return nil, 0, false, false, nil
			}
			return nil, consumed, false, oversized, nil
		default:
			return nil, consumed, false, oversized, err
		}
	}
}

func safeOversizedRecord(prefix []byte) bool {
	matches := recordTypePattern.FindAllSubmatch(prefix, 2)
	return len(matches) == 2 &&
		string(matches[0][1]) == "event_msg" &&
		string(matches[1][1]) == "patch_apply_end"
}
