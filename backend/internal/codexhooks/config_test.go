package codexhooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestInstallMergesUpdatesAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	existing := `{
  "description": "keep me",
  "hooks": {
    "SessionStart": [{"matcher":"resume","hooks":[{"type":"command","command":"echo user"}]}],
    "CustomEvent": [{"hooks":[{"type":"command","command":"echo custom"}]}]
  }
}`
	if err := os.WriteFile(path, []byte(existing), 0o640); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(home, "/tmp/Dora App/dora")
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Install()
	if err != nil {
		t.Fatalf("Install() 失败: %v", err)
	}
	if !status.Installed || status.Trust != "untrusted" {
		t.Fatalf("安装状态错误: %+v", status)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "keep me") || !strings.Contains(string(first), "echo user") || !strings.Contains(string(first), "CustomEvent") {
		t.Fatalf("Install() 覆盖了用户 hooks: %s", first)
	}
	if strings.Count(string(first), marker) != len(observedEvents) {
		t.Fatalf("Dora hooks 数量错误: %s", first)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("文件权限 = %o，期望 640", info.Mode().Perm())
	}
	if _, err := manager.Install(); err != nil {
		t.Fatalf("第二次 Install() 失败: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("重复安装改变了 hooks.json")
	}

	updated, err := NewManager(home, "/tmp/new-dora")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updated.Install(); err != nil {
		t.Fatalf("更新 Dora 路径失败: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "/tmp/Dora App/dora") || strings.Count(string(data), "/tmp/new-dora") != len(observedEvents) {
		t.Fatalf("Dora 路径没有原子更新: %s", data)
	}
}

func TestPermissionRequestUsesLongTimeoutAndStatusRejectsOldConfig(t *testing.T) {
	home := t.TempDir()
	manager, _ := NewManager(home, "/tmp/dora")
	if _, err := manager.Install(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "hooks.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(root["hooks"], &hooks); err != nil {
		t.Fatal(err)
	}
	for _, spec := range observedEvents {
		groups, err := decodeGroups(hooks[spec.event], spec.event)
		if err != nil || len(groups) == 0 {
			t.Fatalf("%s groups = %d, %v", spec.event, len(groups), err)
		}
		var group struct {
			Hooks []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(groups[len(groups)-1], &group); err != nil || len(group.Hooks) != 1 {
			t.Fatalf("解析 %s Dora hook: %+v, %v", spec.event, group, err)
		}
		if group.Hooks[0].Timeout != spec.timeout {
			t.Fatalf("%s timeout = %d，期望 %d", spec.event, group.Hooks[0].Timeout, spec.timeout)
		}
	}

	updated := regexp.MustCompile(`"timeout"\s*:\s*600`).ReplaceAllString(string(data), `"timeout":2`)
	if updated == string(data) {
		t.Fatal("未找到 PermissionRequest 的 600 秒 timeout")
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status()
	if err != nil || status.Installed || !contains(status.Missing, "PermissionRequest") {
		t.Fatalf("旧 PermissionRequest 配置未被识别: %+v, %v", status, err)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestUninstallPreservesOtherHooks(t *testing.T) {
	home := t.TempDir()
	manager, _ := NewManager(home, "/tmp/dora")
	if _, err := manager.Install(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "hooks.json")
	var root map[string]json.RawMessage
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	var hooks map[string]json.RawMessage
	if err := json.Unmarshal(root["hooks"], &hooks); err != nil {
		t.Fatal(err)
	}
	hooks["SessionStart"] = appendUserGroup(t, hooks["SessionStart"])
	root["hooks"], _ = json.Marshal(hooks)
	data, _ = json.Marshal(root)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	status, err := manager.Uninstall()
	if err != nil {
		t.Fatalf("Uninstall() 失败: %v", err)
	}
	if status.Installed || status.Trust != "not_installed" {
		t.Fatalf("卸载状态错误: %+v", status)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), marker) || !strings.Contains(string(data), "echo preserved") {
		t.Fatalf("卸载结果错误: %s", data)
	}
	if _, err := manager.Uninstall(); err != nil {
		t.Fatalf("重复 Uninstall() 失败: %v", err)
	}
}

func TestBrokenConfigIsReportedWithoutWrite(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	original := []byte(`{"hooks": [`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, _ := NewManager(home, "/tmp/dora")
	status, err := manager.Status()
	if err != nil || status.Broken == "" {
		t.Fatalf("Status() 未报告损坏: %+v, %v", status, err)
	}
	if _, err := manager.Install(); err == nil {
		t.Fatal("Install() 接受了损坏配置")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("Install() 修改了损坏配置")
	}
}

func TestStatusReportsTrustedHashes(t *testing.T) {
	home := t.TempDir()
	manager, _ := NewManager(home, "/tmp/dora")
	if _, err := manager.Install(); err != nil {
		t.Fatal(err)
	}
	var config strings.Builder
	for _, spec := range observedEvents {
		config.WriteString("trusted_hash = \"")
		config.WriteString(normalizedHookHash(spec, manager.command()))
		config.WriteString("\"\n")
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status()
	if err != nil || status.Trust != "trusted" {
		t.Fatalf("信任状态 = %+v, %v", status, err)
	}
}

func TestStatusReportsInvalidExecutablePath(t *testing.T) {
	home := t.TempDir()
	executable := filepath.Join(home, "Dora App", "dora")
	manager, _ := NewManager(home, executable)
	status, err := manager.Install()
	if err != nil || status.ExecutableProblem == "" {
		t.Fatalf("缺失 handler 路径状态 = %+v, %v", status, err)
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status()
	if err != nil || status.ExecutableProblem != "" {
		t.Fatalf("可执行 handler 被误报: %+v, %v", status, err)
	}
}

func TestStatusIgnoresCommentedTrustedHashes(t *testing.T) {
	home := t.TempDir()
	manager, _ := NewManager(home, "/tmp/dora")
	if _, err := manager.Install(); err != nil {
		t.Fatal(err)
	}
	var config strings.Builder
	for index, spec := range observedEvents {
		if index > 0 {
			config.WriteString("# ")
		}
		config.WriteString("trusted_hash = \"")
		config.WriteString(normalizedHookHash(spec, manager.command()))
		config.WriteString("\"\n")
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status()
	if err != nil || status.Trust != "partial" {
		t.Fatalf("部分有效、其余注释的 trust = %+v, %v", status, err)
	}

	commented := strings.ReplaceAll(config.String(), "trusted_hash", "# trusted_hash")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(commented), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status()
	if err != nil || status.Trust != "untrusted" {
		t.Fatalf("注释 hash 的 trust = %+v, %v", status, err)
	}
}

func TestNormalizedHookHashesMatchCodexAppServer(t *testing.T) {
	command := "'/Users/wbh/Desktop/dora/bin/dora' hooks emit codex # " + marker
	want := map[string]string{
		"SessionStart":      "sha256:6e9bae606f85b3072bcb8bdf8b14d58280575ef9eb3f56fef0b7f124d49415d8",
		"SessionEnd":        "sha256:b7ef64701404ea53c30d39c7c363a13c79d005d469dd47e1ed5d44598bb8e4b8",
		"UserPromptSubmit":  "sha256:10fffb0d93c6f2bfbebf6478dacd59601097730f2b2a05b96bcbd302e03d93bb",
		"PermissionRequest": "sha256:df3c171f7ef9f97bc9b72c1c8849e4107df09216f34077c05f1fab8cf71548f4",
		"PreToolUse":        "sha256:67ef453a4a5a430b88112cc66edf6dde4a9c1d7817339fa114b04898b3dd408f",
		"PostToolUse":       "sha256:c4c47fbab38e08a44080a735a6bd60dfaf96c264bbdf3172b7a71f4fb4d28933",
		"Stop":              "sha256:0433848855d35aa12b97977398df047612a0efb81d989d474a7f90980d525051",
	}
	for _, spec := range observedEvents {
		if got := normalizedHookHash(spec, command); got != want[spec.event] {
			t.Fatalf("%s hash = %s，Codex hooks/list 返回 %s", spec.event, got, want[spec.event])
		}
	}
}

func appendUserGroup(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	groups, err := decodeGroups(raw, "SessionStart")
	if err != nil {
		t.Fatal(err)
	}
	groups = append(groups, json.RawMessage(`{"hooks":[{"type":"command","command":"echo preserved"}]}`))
	encoded, err := json.Marshal(groups)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
