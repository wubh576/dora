package buildinfo

import "testing"

func TestInfoFormatting(t *testing.T) {
	info := New(
		"v1.2.3",
		"0123456789abcdef0123456789abcdef01234567",
		"2026-07-31T08:00:00Z",
		"go1.26.5",
		"darwin",
		"arm64",
		"15.6",
	)
	want := "Dora v1.2.3\n" +
		"Git commit: 0123456789abcdef0123456789abcdef01234567\n" +
		"Build time: 2026-07-31T08:00:00Z\n" +
		"Go version: go1.26.5\n" +
		"Platform: darwin/arm64\n" +
		"macOS version: 15.6"
	if got := info.String(); got != want {
		t.Fatalf("Info.String() = %q, want %q", got, want)
	}
	if got := info.LogString(); got != "version=v1.2.3 commit=0123456789abcdef0123456789abcdef01234567 build_time=2026-07-31T08:00:00Z go=go1.26.5 platform=darwin/arm64 macos=15.6" {
		t.Fatalf("Info.LogString() = %q", got)
	}
}

func TestInfoDefaults(t *testing.T) {
	info := New("", "", "", "", "", "", "")
	if info.Version != "dev+unknown" || info.GitCommit != unknown || info.BuildTime != unknown {
		t.Fatalf("build 缺省值错误: %+v", info)
	}
	if info.GoVersion != unknown || info.Platform() != "unknown/unknown" || info.MacOSVersion != unknown {
		t.Fatalf("环境缺省值错误: %+v", info)
	}
}

func TestDevelopmentVersionUsesShortCommit(t *testing.T) {
	info := New("", "0123456789abcdef", "", "go1.26.5", "darwin", "arm64", "15.6")
	if info.Version != "dev+0123456789ab" {
		t.Fatalf("开发版本 = %q", info.Version)
	}
}
