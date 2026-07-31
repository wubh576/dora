package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveHomesPriorityAndDeduplication(t *testing.T) {
	t.Setenv("CODEX_HOME", "/tmp/environment-codex")
	configured := filepath.Join(t.TempDir(), "configured")
	homes, err := ResolveHomes([]string{configured, configured})
	if err != nil {
		t.Fatalf("ResolveHomes() 失败: %v", err)
	}
	if len(homes) != 1 || homes[0] != configured {
		t.Fatalf("配置路径优先级错误: %v", homes)
	}

	homes, err = ResolveHomes(nil)
	if err != nil {
		t.Fatalf("ResolveHomes() 环境变量失败: %v", err)
	}
	if len(homes) != 1 || homes[0] != "/tmp/environment-codex" {
		t.Fatalf("CODEX_HOME 未生效: %v", homes)
	}
}

func TestDiscoverRecursivelyFindsSupportedFiles(t *testing.T) {
	home := t.TempDir()
	paths := []string{
		filepath.Join(home, "sessions", "2026", "usage.jsonl"),
		filepath.Join(home, "sessions", "usage.jsonl.gz"),
		filepath.Join(home, "archived_sessions", "usage.jsonl.zst"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("创建 fixture 目录失败: %v", err)
		}
		if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
			t.Fatalf("创建 fixture 失败: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "sessions", "ignored.json"), []byte{}, 0o600); err != nil {
		t.Fatalf("创建无关 fixture 失败: %v", err)
	}

	files, err := Discover([]string{home})
	if err != nil {
		t.Fatalf("Discover() 失败: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("发现文件数 = %d，期望 3: %+v", len(files), files)
	}
	if files[0].Path > files[1].Path || files[1].Path > files[2].Path {
		t.Fatalf("文件顺序不稳定: %+v", files)
	}
	if !files[1].Compressed && !files[2].Compressed {
		t.Fatalf("压缩文件未标记: %+v", files)
	}
}

func TestDiscoverAllowsMissingDirectories(t *testing.T) {
	files, err := Discover([]string{t.TempDir()})
	if err != nil {
		t.Fatalf("缺失默认目录不应报错: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("发现文件数 = %d，期望 0", len(files))
	}
}
