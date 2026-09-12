package workbuddyhooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkBuddyInstallIsIdempotentAndUninstallPreservesUserSettings(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "dora's binary")
	m, err := NewManager(home, binary)
	if err != nil {
		t.Fatal(err)
	}
	original := `{"permissions":{"defaultMode":"default"},"enabledPlugins":{"user@market":true},"hooks":{"Custom":[],"Stop":[{"matcher":"","hooks":[{"type":"command","command":"echo user-hook","timeout":5}]}]}}`
	if err := os.WriteFile(m.path(), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := m.Install()
	if err != nil || !status.Installed {
		t.Fatalf("Install: %+v %v", status, err)
	}
	before, _ := os.ReadFile(m.path())
	if _, err := m.Install(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(m.path())
	if string(before) != string(after) {
		t.Fatal("重复安装改变配置")
	}
	if !strings.Contains(m.command(), `'"'"'`) {
		t.Fatal("路径中的引号没有转义")
	}
	status, err = m.Uninstall()
	if err != nil || status.Installed {
		t.Fatalf("Uninstall: %+v %v", status, err)
	}
	after, _ = os.ReadFile(m.path())
	var want, got any
	json.Unmarshal([]byte(original), &want)
	json.Unmarshal(after, &got)
	w, _ := json.Marshal(want)
	g, _ := json.Marshal(got)
	if string(w) != string(g) {
		t.Fatalf("用户配置发生变化: %s", g)
	}
}
func TestWorkBuddyBrokenConfigIsNeverOverwritten(t *testing.T) {
	for _, input := range []string{`{broken`, `null`, `{"hooks":[]}`, `{"hooks":{"Stop":[{"hooks":"bad"}]}}`} {
		home := t.TempDir()
		m, _ := NewManager(home, "/tmp/dora")
		os.WriteFile(m.path(), []byte(input), 0600)
		if _, err := m.Install(); err == nil {
			t.Fatalf("应拒绝 %s", input)
		}
		got, _ := os.ReadFile(m.path())
		if string(got) != input {
			t.Fatal("损坏配置被覆盖")
		}
	}
}
func TestWorkBuddyUninstallMissingConfigDoesNotCreateFiles(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	m, _ := NewManager(home, "/tmp/dora")
	if _, err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("卸载不应创建目录")
	}
}
