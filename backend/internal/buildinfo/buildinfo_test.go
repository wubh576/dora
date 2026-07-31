package buildinfo

import "testing"

func TestInfoFormatting(t *testing.T) {
	info := New(
		"0123456789abcdef0123456789abcdef01234567",
		true,
		"2026-07-31T08:00:00Z",
		"go1.26.5",
		"darwin",
		"arm64",
		"15.6",
	)
	if got := info.BuildID(); got != "0123456789ab-dirty" {
		t.Fatalf("BuildID() = %q", got)
	}
	if got := info.LogString(); got != "build=0123456789ab-dirty commit=0123456789abcdef0123456789abcdef01234567 dirty=true build_time=2026-07-31T08:00:00Z go=go1.26.5 platform=darwin/arm64 macos=15.6" {
		t.Fatalf("Info.LogString() = %q", got)
	}
}

func TestInfoDefaults(t *testing.T) {
	info := New("", false, "", "", "", "", "")
	if info.BuildID() != unknown || info.GitCommit != unknown || info.BuildTime != unknown || info.Dirty {
		t.Fatalf("build 缺省值错误: %+v", info)
	}
	if info.GoVersion != unknown || info.Platform() != "unknown/unknown" || info.MacOSVersion != unknown {
		t.Fatalf("环境缺省值错误: %+v", info)
	}
}

func TestCleanBuildUsesShortCommit(t *testing.T) {
	info := New("0123456789abcdef", false, "", "go1.26.5", "darwin", "arm64", "15.6")
	if info.BuildID() != "0123456789ab" {
		t.Fatalf("构建标识 = %q", info.BuildID())
	}
}
