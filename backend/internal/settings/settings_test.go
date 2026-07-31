package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreDefaultsAndPersistsWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Dora", "settings.json")
	store := New(path)

	initial, err := store.Load()
	if err != nil {
		t.Fatalf("Load() 默认设置失败: %v", err)
	}
	if !initial.CodexQuotaConsent {
		t.Fatal("quota consent 默认应开启")
	}
	if err := store.Save(Values{CodexQuotaConsent: false}); err != nil {
		t.Fatalf("Save() 失败: %v", err)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatalf("Load() 已保存设置失败: %v", err)
	}
	if saved.CodexQuotaConsent {
		t.Fatal("用户关闭 quota consent 后未持久化")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取设置文件失败: %v", err)
	}
	if permission := info.Mode().Perm(); permission != 0o600 {
		t.Fatalf("设置权限 = %o，期望 600", permission)
	}
}

func TestStoreUsesDefaultForMissingConsentField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("写入旧设置 fixture 失败: %v", err)
	}
	values, err := New(path).Load()
	if err != nil {
		t.Fatalf("加载旧设置失败: %v", err)
	}
	if !values.CodexQuotaConsent {
		t.Fatal("缺少 consent 字段时应使用开启默认值")
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
