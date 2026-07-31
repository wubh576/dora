package buildinfo

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

const unknown = "unknown"

// 这三个值由生产构建通过 -ldflags 注入；go run 和测试使用安全缺省值。
var (
	version   string
	commit    string
	buildTime string
)

type Info struct {
	Version      string `json:"version"`
	GitCommit    string `json:"gitCommit"`
	BuildTime    string `json:"buildTime"`
	GoVersion    string `json:"goVersion"`
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
	MacOSVersion string `json:"macOSVersion"`
}

func Current() Info {
	return New(version, commit, buildTime, runtime.Version(), runtime.GOOS, runtime.GOARCH, macOSProductVersion())
}

func New(version, commit, buildTime, goVersion, goos, goarch, macOSVersion string) Info {
	commit = valueOrUnknown(commit)
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev+" + shortCommit(commit)
	}
	return Info{
		Version:      version,
		GitCommit:    commit,
		BuildTime:    valueOrUnknown(buildTime),
		GoVersion:    valueOrUnknown(goVersion),
		GOOS:         valueOrUnknown(goos),
		GOARCH:       valueOrUnknown(goarch),
		MacOSVersion: valueOrUnknown(macOSVersion),
	}
}

func (i Info) Platform() string {
	return i.GOOS + "/" + i.GOARCH
}

func (i Info) String() string {
	return fmt.Sprintf(
		"Dora %s\nGit commit: %s\nBuild time: %s\nGo version: %s\nPlatform: %s\nmacOS version: %s",
		i.Version,
		i.GitCommit,
		i.BuildTime,
		i.GoVersion,
		i.Platform(),
		i.MacOSVersion,
	)
}

func (i Info) LogString() string {
	return fmt.Sprintf(
		"version=%s commit=%s build_time=%s go=%s platform=%s macos=%s",
		i.Version,
		i.GitCommit,
		i.BuildTime,
		i.GoVersion,
		i.Platform(),
		i.MacOSVersion,
	)
}

func valueOrUnknown(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return unknown
}

func shortCommit(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func macOSProductVersion() string {
	if runtime.GOOS != "darwin" {
		return unknown
	}
	output, err := exec.Command("/usr/bin/sw_vers", "-productVersion").Output()
	if err != nil {
		return unknown
	}
	return valueOrUnknown(string(output))
}
