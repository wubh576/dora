package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/provider/claudecode"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

func TestMultiProviderScannerCollectsClaudeIncrementally(t *testing.T) {
	ctx := context.Background()
	codexHome := t.TempDir()
	claudeHome := t.TempDir()
	mainPath := filepath.Join(claudeHome, "projects", "-tmp-dora", "main.jsonl")
	subagentPath := filepath.Join(claudeHome, "projects", "-tmp-dora", "main", "subagents", "agent-one.jsonl")
	copyClaudeFixture(t, "streaming.jsonl", mainPath)
	copyClaudeFixture(t, "fork.jsonl", subagentPath)
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := NewWithClaude(store, []string{codexHome}, []string{claudeHome})

	first, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("首次多 provider 扫描失败: %v", err)
	}
	claudeReport := reportForSource(t, first, domain.ClaudeCodeSource)
	if first.Mode != "full" || claudeReport.FilesSeen != 2 || claudeReport.SessionCount != 1 || claudeReport.EventsStored != 2 {
		t.Fatalf("首次 Claude 扫描结果错误: total=%+v claude=%+v", first, claudeReport)
	}
	checkpoints, err := store.LoadSourceFiles(ctx, domain.ClaudeCodeSource)
	if err != nil || len(checkpoints) != 2 {
		t.Fatalf("读取 Claude checkpoint 失败: checkpoints=%+v err=%v", checkpoints, err)
	}
	for _, checkpoint := range checkpoints {
		if strings.Contains(checkpoint.Path, string(filepath.Separator)) || strings.Contains(checkpoint.Path, "main") {
			t.Fatalf("Claude checkpoint 泄漏 transcript/session 路径: %q", checkpoint.Path)
		}
	}
	second, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("Claude 增量空扫描失败: %v", err)
	}
	if report := reportForSource(t, second, domain.ClaudeCodeSource); report.Mode != "incremental" || report.EventsSeen != 0 || report.EventsStored != 2 {
		t.Fatalf("Claude 增量空扫描错误: %+v", report)
	}

	appendFile(t, mainPath, []byte(`{"type":"assistant","uuid":"new-local","timestamp":"2026-07-31T09:02:00Z","cwd":"/tmp/dora","message":{"id":"msg-new","role":"assistant","model":"model-switched","content":[{"type":"text","text":"new"}],"usage":{"input_tokens":4,"output_tokens":6}}}`+"\n"))
	third, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("Claude 增量追加失败: %v", err)
	}
	if report := reportForSource(t, third, domain.ClaudeCodeSource); report.Mode != "incremental" || report.EventsStored != 3 {
		t.Fatalf("Claude 增量追加结果错误: %+v", report)
	}
	events, err := store.LoadUsageEvents(ctx, domain.ClaudeCodeSource)
	if err != nil || len(events) != 3 || events[2].Model != "model-switched" {
		t.Fatalf("Claude model switch 未保存: events=%+v err=%v", events, err)
	}
	copyClaudeFixture(t, "basic.jsonl", mainPath)
	rebuilt, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("Claude replace 重建失败: %v", err)
	}
	if provider := reportForSource(t, rebuilt, domain.ClaudeCodeSource); provider.Mode != "full" || provider.EventsStored != 5 {
		t.Fatalf("Claude replace 未触发全量重建: %+v", provider)
	}
}

func TestMultiProviderScannerFailureIsolationAndMissingClaude(t *testing.T) {
	ctx := context.Background()
	codexHome := t.TempDir()
	claudeHome := t.TempDir()
	mainPath := filepath.Join(claudeHome, "projects", "-tmp-dora", "main.jsonl")
	copyClaudeFixture(t, "basic.jsonl", mainPath)
	store, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "dora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := NewWithClaude(store, []string{codexHome}, []string{claudeHome})
	if _, err := scanner.Scan(ctx, false); err != nil {
		t.Fatalf("基线扫描失败: %v", err)
	}
	baseline, _ := store.LoadUsageEvents(ctx, domain.ClaudeCodeSource)
	offlineHome := claudeHome + "-offline"
	if err := os.Rename(claudeHome, offlineHome); err != nil {
		t.Fatal(err)
	}
	offlineReport, err := scanner.Scan(ctx, false)
	if err != nil {
		t.Fatalf("Claude config 暂时消失不应使整体扫描失败: %v", err)
	}
	if report := reportForSource(t, offlineReport, domain.ClaudeCodeSource); report.Mode != "preserve" || report.EventsStored != len(baseline) {
		t.Fatalf("Claude config 消失未保留旧 generation: %+v", report)
	}
	preserved, _ := store.LoadUsageEvents(ctx, domain.ClaudeCodeSource)
	if len(preserved) != len(baseline) {
		t.Fatalf("Claude config 消失清空旧数据: before=%d after=%d", len(baseline), len(preserved))
	}
	if err := os.Rename(offlineHome, claudeHome); err != nil {
		t.Fatal(err)
	}
	invalid := `{"type":"assistant","uuid":"bad","timestamp":"2026-07-31T10:00:00Z","message":{"id":"bad","role":"assistant","model":"model","usage":{"input_tokens":-1}}}` + "\n"
	if err := os.WriteFile(mainPath, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := scanner.Scan(ctx, false)
	if err == nil || !strings.Contains(err.Error(), domain.ClaudeCodeSource) {
		t.Fatalf("Claude failure 未按 provider 返回: report=%+v err=%v", report, err)
	}
	if codex := reportForSource(t, report, domain.CodexSource); codex.Error != "" {
		t.Fatalf("Claude failure 污染 Codex: %+v", codex)
	}
	after, _ := store.LoadUsageEvents(ctx, domain.ClaudeCodeSource)
	if len(after) != len(baseline) {
		t.Fatalf("Claude failure 清空最后成功 generation: before=%d after=%d", len(baseline), len(after))
	}
	claudeState, err := store.UsageProviderState(ctx, domain.ClaudeCodeSource)
	if err != nil || claudeState.Status != "error" {
		t.Fatalf("Claude failure 状态错误: state=%+v err=%v", claudeState, err)
	}
	codexState, err := store.UsageProviderState(ctx, domain.CodexSource)
	if err != nil || codexState.Status != "ready" {
		t.Fatalf("Codex 成功状态丢失: state=%+v err=%v", codexState, err)
	}

	missingStore, err := dorasqlite.Open(ctx, filepath.Join(t.TempDir(), "missing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer missingStore.Close()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := NewWithClaude(missingStore, []string{t.TempDir()}, []string{missing}).Scan(ctx, false); err != nil {
		t.Fatalf("缺失 Claude config 不应失败: %v", err)
	}
	state, err := missingStore.UsageProviderState(ctx, domain.ClaudeCodeSource)
	if err != nil || state.Status != "not_found" || state.ConfigFound || state.SessionCount != 0 || state.ParserVersion != claudecode.ParserVersion {
		t.Fatalf("缺失 Claude config diagnostics 错误: state=%+v err=%v", state, err)
	}
}

func reportForSource(t *testing.T, report Report, source string) ProviderReport {
	t.Helper()
	for _, provider := range report.Providers {
		if provider.Source == source {
			return provider
		}
	}
	t.Fatalf("缺少 provider report %s: %+v", source, report)
	return ProviderReport{}
}

func copyClaudeFixture(t *testing.T, name, destination string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "provider", "claudecode", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
