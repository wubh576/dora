package claudecode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveHomesPriorityAndDiscoverSessions(t *testing.T) {
	environment := filepath.Join(t.TempDir(), "environment")
	t.Setenv("CLAUDE_CONFIG_DIR", environment)
	homes, err := ResolveHomes(nil)
	if err != nil || len(homes) != 1 || homes[0] != environment {
		t.Fatalf("CLAUDE_CONFIG_DIR 未生效: homes=%v err=%v", homes, err)
	}
	configured := filepath.Join(t.TempDir(), "configured")
	homes, err = ResolveHomes([]string{configured, configured})
	if err != nil || len(homes) != 1 || homes[0] != configured {
		t.Fatalf("显式配置优先级错误: homes=%v err=%v", homes, err)
	}

	paths := []string{
		filepath.Join(configured, "projects", "-tmp-dora", "main.jsonl"),
		filepath.Join(configured, "projects", "-tmp-dora", "main", "subagents", "agent-one.jsonl"),
		filepath.Join(configured, "projects", "-tmp-other", "second.jsonl"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("创建 fixture 目录失败: %v", err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("创建 fixture 失败: %v", err)
		}
	}
	files, err := Discover(homes)
	if err != nil {
		t.Fatalf("Discover() 失败: %v", err)
	}
	if len(files) != 3 || SessionCount(files) != 2 {
		t.Fatalf("发现结果错误: files=%+v sessions=%d", files, SessionCount(files))
	}
	if files[0].Path > files[1].Path || files[1].Path > files[2].Path {
		t.Fatalf("发现顺序不稳定: %+v", files)
	}
	foundSubagent := false
	for _, file := range files {
		foundSubagent = foundSubagent || file.Subagent
	}
	if !foundSubagent {
		t.Fatal("未识别 subagent transcript")
	}
}

func TestDiscoverAllowsMissingProjectsDirectory(t *testing.T) {
	files, err := Discover([]string{t.TempDir()})
	if err != nil || len(files) != 0 {
		t.Fatalf("缺失 projects 目录应返回空结果: files=%v err=%v", files, err)
	}
}

func TestSnapshotDetectsAppendReplaceAndTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := File{Path: path}
	snapshot, err := Inspect(file)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = handle.WriteString("third\n")
	_ = handle.Close()
	if matches, err := MatchesSnapshot(file, snapshot); err != nil || !matches {
		t.Fatalf("append snapshot 校验失败: matches=%v err=%v", matches, err)
	}
	if safe, err := MatchesAppendPrefix(file, snapshot); err != nil || !safe {
		t.Fatalf("append prefix 校验失败: safe=%v err=%v", safe, err)
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if matches, err := MatchesSnapshot(file, snapshot); err != nil || matches {
		t.Fatalf("replace 未被识别: matches=%v err=%v", matches, err)
	}
}
