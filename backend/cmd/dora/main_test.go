package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/quota"
	"github.com/wubh576/dora/backend/internal/scan"
	"github.com/wubh576/dora/backend/internal/settings"
)

func TestValidateLoopbackAddress(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{name: "loopback", addr: "127.0.0.1:8080"},
		{name: "all interfaces", addr: "0.0.0.0:8080", wantErr: true},
		{name: "invalid", addr: "127.0.0.1", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateLoopbackAddress(test.addr)
			if test.wantErr && err == nil {
				t.Fatal("validateLoopbackAddress() 未返回错误")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validateLoopbackAddress() 返回错误: %v", err)
			}
		})
	}
}

func TestRunManualScanWithConfiguredHome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dora.db")
	if err := run([]string{
		"scan",
		"--db", path,
		"--codex-home", t.TempDir(),
	}); err != nil {
		t.Fatalf("run(scan) 失败: %v", err)
	}
}

func TestRunManualScanReportsInvalidFilePath(t *testing.T) {
	home := t.TempDir()
	sessionPath := filepath.Join(home, "sessions", "broken.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatalf("创建 session 目录失败: %v", err)
	}
	content := `{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":-1}}}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(content), 0o600); err != nil {
		t.Fatalf("写入无效 session 失败: %v", err)
	}

	err := run([]string{
		"scan",
		"--db", filepath.Join(t.TempDir(), "dora.db"),
		"--codex-home", home,
	})
	if err == nil || !strings.Contains(err.Error(), sessionPath) {
		t.Fatalf("scan 错误未包含目标文件路径: %v", err)
	}
}

func TestQuotaCommandHonorsDisabledConsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dora.db")
	if err := settings.New(filepath.Join(filepath.Dir(path), "settings.json")).Save(
		settings.Values{CodexQuotaConsent: false},
	); err != nil {
		t.Fatalf("保存关闭 consent 失败: %v", err)
	}
	err := run([]string{
		"quota",
		"refresh",
		"--db", path,
		"--codex-home", t.TempDir(),
	})
	if err == nil || err.Error() != "Codex 订阅配额尚未授权，请先在 Dora Diagnostics 中启用" {
		t.Fatalf("未授权 quota refresh 错误 = %v", err)
	}
}

func TestUsageScanLoopRunsImmediatelyAndOnInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeUsageScanner{called: make(chan struct{}, 3)}
	done := make(chan struct{})
	go func() {
		runUsageScanLoop(ctx, runner, 5*time.Millisecond)
		close(done)
	}()

	for index := 0; index < 2; index++ {
		select {
		case <-runner.called:
		case <-time.After(time.Second):
			t.Fatal("等待自动扫描超时")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("自动扫描循环未停止")
	}
}

func TestQuotaRefreshLoopRunsImmediatelyAndOnInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeQuotaRefresher{called: make(chan struct{}, 3)}
	done := make(chan struct{})
	go func() {
		runQuotaRefreshLoop(ctx, runner, 5*time.Millisecond)
		close(done)
	}()

	for index := 0; index < 2; index++ {
		select {
		case <-runner.called:
		case <-time.After(time.Second):
			t.Fatal("等待 quota 自动刷新超时")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("quota 自动刷新循环未停止")
	}
}

type fakeUsageScanner struct {
	mu     sync.Mutex
	count  int
	called chan struct{}
}

func (f *fakeUsageScanner) Scan(context.Context, bool) (scan.Report, error) {
	f.mu.Lock()
	f.count++
	f.mu.Unlock()
	f.called <- struct{}{}
	return scan.Report{Mode: "incremental"}, nil
}

type fakeQuotaRefresher struct {
	called chan struct{}
}

func (f *fakeQuotaRefresher) Refresh(context.Context, bool) (quota.View, error) {
	f.called <- struct{}{}
	return quota.View{}, nil
}
