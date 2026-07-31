package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wubh576/dora/backend/internal/domain"
)

const (
	ParserVersion       = 1
	defaultMaxLineBytes = 8 * 1024 * 1024
)

type ParserState struct {
	HomeKey string              `json:"homeKey"`
	Pending *messageAccumulator `json:"pending,omitempty"`
}

type messageAccumulator struct {
	DedupKey      string          `json:"dedupKey"`
	OccurredAt    time.Time       `json:"occurredAt"`
	Model         string          `json:"model"`
	Project       string          `json:"project"`
	Usage         normalizedUsage `json:"usage"`
	ThinkingChars int64           `json:"thinkingChars"`
	OtherChars    int64           `json:"otherChars"`
}

type normalizedUsage struct {
	Input           int64 `json:"input"`
	Output          int64 `json:"output"`
	CacheRead       int64 `json:"cacheRead"`
	CacheCreation   int64 `json:"cacheCreation"`
	Reasoning       int64 `json:"reasoning"`
	ReportedTotal   int64 `json:"reportedTotal"`
	Total           int64 `json:"total"`
	NativeReasoning bool  `json:"nativeReasoning"`
	Completeness    int   `json:"completeness"`
}

type ParseResult struct {
	Events          []domain.UsageEvent
	State           ParserState
	CompleteLineEnd int64
	LinesSeen       int
	Warnings        []string
}

type Parser struct{ MaxLineBytes int }

func NewParser() Parser { return Parser{MaxLineBytes: defaultMaxLineBytes} }

func (p Parser) ParseFileSnapshot(ctx context.Context, file File, offset, snapshotEnd int64, state ParserState) (ParseResult, error) {
	if offset < 0 || snapshotEnd < offset {
		return ParseResult{}, errors.New("Claude Code parser snapshot 范围无效")
	}
	if p.MaxLineBytes <= 0 {
		p.MaxLineBytes = defaultMaxLineBytes
	}
	handle, err := os.Open(file.Path)
	if err != nil {
		return ParseResult{}, fmt.Errorf("打开 Claude Code transcript: %w", err)
	}
	defer handle.Close()
	if _, err := handle.Seek(offset, io.SeekStart); err != nil {
		return ParseResult{}, fmt.Errorf("定位 Claude Code transcript: %w", err)
	}
	if state.HomeKey == "" {
		state.HomeKey = file.HomeKey
	}
	result := ParseResult{State: state, CompleteLineEnd: offset}
	reader := bufio.NewReaderSize(io.LimitReader(handle, snapshotEnd-offset), 64*1024)
	position := offset
	for {
		select {
		case <-ctx.Done():
			return ParseResult{}, ctx.Err()
		default:
		}
		line, consumed, complete, oversized, err := readBoundedLine(reader, p.MaxLineBytes)
		if err != nil {
			return ParseResult{}, fmt.Errorf("读取 Claude Code JSONL: %w", err)
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
			return ParseResult{}, fmt.Errorf("Claude Code JSONL 第 %d 行超过 %d 字节", result.LinesSeen, p.MaxLineBytes)
		}
		candidate, emitted, warnings, err := parseRecord(line, file)
		if err != nil {
			return ParseResult{}, fmt.Errorf("解析 Claude Code JSONL 第 %d 行: %w", result.LinesSeen, err)
		}
		result.Warnings = append(result.Warnings, warnings...)
		if !emitted {
			continue
		}
		if result.State.Pending != nil && result.State.Pending.DedupKey != candidate.DedupKey {
			if event, ok := result.State.Pending.event(); ok {
				result.Events = append(result.Events, event)
			}
			result.State.Pending = nil
		}
		if result.State.Pending == nil {
			result.State.Pending = &candidate
		} else {
			result.State.Pending.merge(candidate)
		}
	}
	if position != snapshotEnd {
		return ParseResult{}, errors.New("Claude Code transcript 在扫描期间变短")
	}
	if result.State.Pending != nil {
		if event, ok := result.State.Pending.event(); ok {
			result.Events = append(result.Events, event)
		}
	}
	return result, nil
}

type envelope struct {
	Timestamp string  `json:"timestamp"`
	Type      string  `json:"type"`
	CWD       string  `json:"cwd"`
	Message   message `json:"message"`
}

type message struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   json.RawMessage `json:"usage"`
}

type contentBlock struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
	Text     string `json:"text"`
}

type rawUsage struct {
	Input         *int64 `json:"input_tokens"`
	Output        *int64 `json:"output_tokens"`
	CacheRead     *int64 `json:"cache_read_input_tokens"`
	CacheCreation *int64 `json:"cache_creation_input_tokens"`
	Reasoning     *int64 `json:"reasoning_output_tokens"`
	Total         *int64 `json:"total_tokens"`
}

func parseRecord(line []byte, file File) (messageAccumulator, bool, []string, error) {
	var record envelope
	if err := json.Unmarshal(line, &record); err != nil {
		return messageAccumulator{}, false, nil, err
	}
	if record.Type != "assistant" || record.Message.Role != "assistant" || len(record.Message.Usage) == 0 || string(record.Message.Usage) == "null" {
		return messageAccumulator{}, false, nil, nil
	}
	stableID := strings.TrimSpace(record.Message.ID)
	if stableID == "" {
		return messageAccumulator{}, false, []string{"Claude Code assistant usage 缺少 message ID，已跳过以避免 streaming 重复计数"}, nil
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil {
		return messageAccumulator{}, false, nil, errors.New("assistant usage 时间戳无效")
	}
	usage, err := normalizeUsage(record.Message.Usage)
	if err != nil {
		return messageAccumulator{}, false, nil, err
	}
	model := strings.TrimSpace(record.Message.Model)
	if model == "" {
		model = "unknown"
	}
	project := file.Project
	if cwd := strings.TrimSpace(record.CWD); cwd != "" {
		if base := filepath.Base(filepath.Clean(cwd)); base != "." && base != string(filepath.Separator) {
			project = base
		}
	}
	if project == "" {
		project = "unknown"
	}
	var blocks []contentBlock
	if len(record.Message.Content) > 0 && record.Message.Content[0] == '[' {
		if err := json.Unmarshal(record.Message.Content, &blocks); err != nil {
			return messageAccumulator{}, false, nil, err
		}
	}
	var thinkingChars, otherChars int64
	for _, block := range blocks {
		if block.Type == "thinking" {
			thinkingChars += int64(utf8.RuneCountInString(block.Thinking))
		} else if block.Type == "text" {
			otherChars += int64(utf8.RuneCountInString(block.Text))
		}
	}
	return messageAccumulator{
		DedupKey:   stableHash("claude-message|" + file.HomeKey + "|" + stableID),
		OccurredAt: occurredAt.UTC(), Model: model, Project: project, Usage: usage,
		ThinkingChars: thinkingChars, OtherChars: otherChars,
	}, true, nil, nil
}

func normalizeUsage(raw json.RawMessage) (normalizedUsage, error) {
	var usage rawUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return normalizedUsage{}, err
	}
	values := []*int64{usage.Input, usage.Output, usage.CacheRead, usage.CacheCreation, usage.Reasoning, usage.Total}
	for _, value := range values {
		if value != nil && *value < 0 {
			return normalizedUsage{}, errors.New("assistant usage token 不能为负数")
		}
	}
	result := normalizedUsage{
		Input: valueOrZero(usage.Input), Output: valueOrZero(usage.Output),
		CacheRead: valueOrZero(usage.CacheRead), CacheCreation: valueOrZero(usage.CacheCreation),
		Reasoning: valueOrZero(usage.Reasoning), ReportedTotal: valueOrZero(usage.Total),
		NativeReasoning: usage.Reasoning != nil && valueOrZero(usage.Reasoning) > 0,
	}
	for _, value := range values {
		if value != nil {
			result.Completeness++
		}
	}
	detail, err := checkedSum(result.Input, result.Output, result.CacheRead, result.CacheCreation, result.Reasoning)
	if err != nil {
		return normalizedUsage{}, err
	}
	result.Total = maxInt64(detail, result.ReportedTotal)
	return result, nil
}

func (a *messageAccumulator) merge(next messageAccumulator) {
	if next.OccurredAt.Before(a.OccurredAt) {
		a.OccurredAt = next.OccurredAt
	}
	if a.Model == "unknown" && next.Model != "unknown" {
		a.Model = next.Model
	}
	if a.Project == "unknown" && next.Project != "unknown" {
		a.Project = next.Project
	}
	if preferUsage(next.Usage, a.Usage) {
		a.Usage = next.Usage
	}
	a.ThinkingChars += next.ThinkingChars
	a.OtherChars += next.OtherChars
}

func (a messageAccumulator) event() (domain.UsageEvent, bool) {
	usage := a.Usage
	if usage.Total == 0 && usage.ReportedTotal == 0 {
		return domain.UsageEvent{}, false
	}
	if !usage.NativeReasoning && isKnownAnthropicModel(a.Model) && a.ThinkingChars > 0 {
		allChars := a.ThinkingChars + a.OtherChars
		if allChars > 0 {
			reasoning := int64(math.Round(float64(usage.Output) * float64(a.ThinkingChars) / float64(allChars)))
			if reasoning > usage.Output {
				reasoning = usage.Output
			}
			usage.Output -= reasoning
			usage.Reasoning = reasoning
		}
	}
	return domain.UsageEvent{
		Source: domain.ClaudeCodeSource, DedupKey: a.DedupKey, OccurredAt: a.OccurredAt,
		Model: a.Model, Project: a.Project,
		InputTokens: usage.Input, OutputTokens: usage.Output,
		CachedInputTokens: usage.CacheRead, CacheCreationInputTokens: usage.CacheCreation,
		ReasoningOutputTokens: usage.Reasoning, ReportedTotalTokens: usage.ReportedTotal,
		TotalTokens: usage.Total,
	}, true
}

func isKnownAnthropicModel(model string) bool {
	value := strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"claude-3-", "claude-opus-4-", "claude-sonnet-4-", "claude-haiku-4-"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func preferUsage(candidate, existing normalizedUsage) bool {
	if candidate.Total != existing.Total {
		return candidate.Total > existing.Total
	}
	return candidate.Completeness > existing.Completeness
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func checkedSum(values ...int64) (int64, error) {
	var total int64
	for _, value := range values {
		if value > math.MaxInt64-total {
			return 0, errors.New("assistant usage token 超出 int64")
		}
		total += value
	}
	return total, nil
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func readBoundedLine(reader *bufio.Reader, maxBytes int) ([]byte, int64, bool, bool, error) {
	var line []byte
	var consumed int64
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		consumed += int64(len(fragment))
		if len(line) < maxBytes+1 {
			remaining := maxBytes + 1 - len(line)
			if len(fragment) < remaining {
				remaining = len(fragment)
			}
			line = append(line, fragment[:remaining]...)
		}
		if consumed > int64(maxBytes) {
			oversized = true
		}
		switch {
		case err == nil:
			return line, consumed, true, oversized, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return line, consumed, false, oversized, nil
		default:
			return nil, consumed, false, oversized, err
		}
	}
}
