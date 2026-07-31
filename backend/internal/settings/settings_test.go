package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreDefaultsPersistsAndUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Dora", "settings.json")
	store := New(path)

	initial, err := store.Load()
	if err != nil {
		t.Fatalf("Load() 默认设置失败: %v", err)
	}
	if initial.CodexQuotaConsent {
		t.Fatal("quota consent 默认应关闭")
	}
	if err := store.Save(Values{CodexQuotaConsent: true}); err != nil {
		t.Fatalf("Save() 失败: %v", err)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatalf("Load() 已保存设置失败: %v", err)
	}
	if !saved.CodexQuotaConsent {
		t.Fatal("quota consent 未持久化")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取设置文件失败: %v", err)
	}
	if permission := info.Mode().Perm(); permission != 0o600 {
		t.Fatalf("设置权限 = %o，期望 600", permission)
	}
}

func TestStoreRejectsUnknownSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"codexQuotaConsent":true,"token":"secret"}`), 0o600); err != nil {
		t.Fatalf("写入设置 fixture 失败: %v", err)
	}
	if _, err := New(path).Load(); err == nil {
		t.Fatal("未知设置字段未被拒绝")
	}
}
