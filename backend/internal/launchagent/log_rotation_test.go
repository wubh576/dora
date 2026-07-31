package launchagent

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultLogRotationValues(t *testing.T) {
	if DefaultLogMaxBytes != 200*1024*1024 {
		t.Fatalf("生产轮转阈值 = %d", DefaultLogMaxBytes)
	}
	if DefaultLogCheckInterval != 10*time.Minute {
		t.Fatalf("生产检查周期 = %s", DefaultLogCheckInterval)
	}
}

func TestLogRotationSkipsSmallAndMissingFiles(t *testing.T) {
	directory := t.TempDir()
	small := filepath.Join(directory, "small.log")
	writeRotationFixture(t, small, "123")
	rotator := NewLogRotator(LogRotationConfig{Files: []string{small, filepath.Join(directory, "missing.log")}, MaxBytes: 4, Logger: log.New(&bytes.Buffer{}, "", 0)})
	rotator.Check()
	if got := readRotationFixture(t, small); got != "123" {
		t.Fatalf("小日志内容被修改: %q", got)
	}
	for _, backup := range []string{small + ".1", filepath.Join(directory, "missing.log.1")} {
		if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("无须轮转时创建了备份 %s: %v", backup, err)
		}
	}
}

func TestLogRotationAtOrAboveThreshold(t *testing.T) {
	for _, content := range []string{"1234", "12345"} {
		t.Run(content, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dora.log")
			writeRotationFixture(t, path, content)
			NewLogRotator(LogRotationConfig{Files: []string{path}, MaxBytes: 4, Logger: log.New(&bytes.Buffer{}, "", 0)}).Check()
			if got := readRotationFixture(t, path+".1"); got != content {
				t.Fatalf("备份内容 = %q，期望 %q", got, content)
			}
			if got := readRotationFixture(t, path); got != "" {
				t.Fatalf("活动日志未清空: %q", got)
			}
		})
	}
}

func TestLogRotationKeepsOpenAppendDescriptorOnActivePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dora.log")
	active, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	if _, err := active.WriteString("before"); err != nil {
		t.Fatal(err)
	}
	NewLogRotator(LogRotationConfig{Files: []string{path}, MaxBytes: 6, Logger: log.New(&bytes.Buffer{}, "", 0)}).Check()
	if _, err := active.WriteString("after"); err != nil {
		t.Fatal(err)
	}
	if err := active.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := readRotationFixture(t, path+".1"); got != "before" {
		t.Fatalf("备份内容 = %q", got)
	}
	if got := readRotationFixture(t, path); got != "after" {
		t.Fatalf("已打开描述符未继续写入活动路径: %q", got)
	}
}

func TestLogRotationOverwritesSingleBackup(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "dora.log")
	writeRotationFixture(t, path, "new1")
	writeRotationFixture(t, path+".1", "old")
	writeRotationFixture(t, path+".1.tmp", "stale")
	rotator := NewLogRotator(LogRotationConfig{Files: []string{path}, MaxBytes: 4, Logger: log.New(&bytes.Buffer{}, "", 0)})
	rotator.Check()
	if got := readRotationFixture(t, path+".1"); got != "new1" {
		t.Fatalf("旧备份未覆盖: %q", got)
	}
	writeRotationFixture(t, path, "new2")
	rotator.Check()
	if got := readRotationFixture(t, path+".1"); got != "new2" {
		t.Fatalf("第二次备份内容 = %q", got)
	}
	if _, err := os.Stat(path + ".2"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("产生了多余历史备份: %v", err)
	}
	if _, err := os.Stat(path + ".1.tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("轮转临时文件未清理: %v", err)
	}
}

func TestLogRotationChecksStdoutAndStderrIndependently(t *testing.T) {
	for _, test := range []struct {
		name       string
		stdoutSize int
		stderrSize int
	}{
		{name: "stdout only", stdoutSize: 4, stderrSize: 3},
		{name: "stderr only", stdoutSize: 3, stderrSize: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			stdout := filepath.Join(directory, "dora.stdout.log")
			stderr := filepath.Join(directory, "dora.stderr.log")
			writeRotationFixture(t, stdout, strings.Repeat("o", test.stdoutSize))
			writeRotationFixture(t, stderr, strings.Repeat("e", test.stderrSize))
			NewLogRotator(LogRotationConfig{Files: []string{stdout, stderr}, MaxBytes: 4, Logger: log.New(&bytes.Buffer{}, "", 0)}).Check()
			assertRotationBackup(t, stdout, test.stdoutSize >= 4)
			assertRotationBackup(t, stderr, test.stderrSize >= 4)
		})
	}
}

func TestLogRotationFailureDoesNotBlockOtherFile(t *testing.T) {
	directory := t.TempDir()
	invalid := filepath.Join(directory, "invalid.log")
	if err := os.Mkdir(invalid, 0o700); err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(directory, "valid.log")
	writeRotationFixture(t, valid, "rotate")
	var output bytes.Buffer
	NewLogRotator(LogRotationConfig{Files: []string{invalid, valid}, MaxBytes: 1, Logger: log.New(&output, "", 0)}).Check()
	if !strings.Contains(output.String(), "Dora 日志轮转失败") || !strings.Contains(output.String(), invalid) || !strings.Contains(output.String(), "将在下次检查重试") {
		t.Fatalf("轮转失败日志不明确: %q", output.String())
	}
	if got := readRotationFixture(t, valid+".1"); got != "rotate" {
		t.Fatalf("一侧失败阻断另一侧轮转: %q", got)
	}
}

func TestLogRotationPeriodicCheckAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dora.log")
	var output bytes.Buffer
	rotator := NewLogRotator(LogRotationConfig{Files: []string{path}, MaxBytes: 4, CheckInterval: 5 * time.Millisecond, Logger: log.New(&output, "", 0)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		rotator.Run(ctx)
		close(done)
	}()
	writeRotationFixture(t, path, "tick")
	waitForRotation(t, path+".1")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("轮转周期任务未随 context cancellation 退出")
	}
}

func writeRotationFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRotationFixture(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func assertRotationBackup(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path + ".1")
	if want && err != nil {
		t.Fatalf("缺少备份 %s: %v", path+".1", err)
	}
	if !want && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("不应存在备份 %s: %v", path+".1", err)
	}
}

func waitForRotation(t *testing.T, backup string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(backup); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待轮转超时: %s", backup)
}
