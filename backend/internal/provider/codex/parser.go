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
	ParserVersion       = 1
	defaultMaxLineBytes = 8 * 1024 * 1024
	linePrefixBytes     = 64 * 1024
)

var recordTypePattern = regexp.MustCompile(`"type"\s*:\s*"([^"]+)"`)

type ParserState struct {
	HomeKey          string `json:"homeKey"`
	RolloutKey       string `json:"rolloutKey"`
	ParentRolloutKey string `json:"parentRolloutKey"`
	Project          string `json:"project"`
	Model            string `json:"model"`
	SawTurnContext   bool   `json:"sawTurnContext"`
	LineageAmbiguous bool   `json:"lineageAmbiguous"`
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
	Input         int64
	Output        int64
	Cached        int64
	CacheCreation int64
	Reasoning     int64
	ReportedTotal int64
	Total         int64
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
		return parseTokenEvent(record, *state)
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
		state.RolloutKey = rolloutKey(state.HomeKey, payload.ID)
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

func parseTokenEvent(record envelope, state ParserState) (parsedEvent, bool, []string, error) {
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

	lastRaw := payload.Info.Last
	if lastRaw == nil {
		lastRaw = payload.Info.Total
	}
	if lastRaw == nil {
		return parsedEvent{}, false, nil, nil
	}
	last, _, err := normalizeUsage(lastRaw)
	if err != nil {
		return parsedEvent{}, false, nil, err
	}

	var total normalizedUsage
	var totalComplete bool
	if payload.Info.Total != nil {
		total, totalComplete, err = normalizeUsage(payload.Info.Total)
		if err != nil {
			return parsedEvent{}, false, nil, err
		}
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
	}, true, nil, nil
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
