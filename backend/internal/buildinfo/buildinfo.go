package buildinfo

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

const unknown = "unknown"

// 这些值由构建通过 -ldflags 注入；go run 和测试使用安全缺省值。
var (
	commit    string
	buildTime string
	dirty     string
)

type Info struct {
	GitCommit    string `json:"gitCommit"`
	BuildTime    string `json:"buildTime"`
	Dirty        bool   `json:"dirty"`
	GoVersion    string `json:"goVersion"`
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
	MacOSVersion string `json:"macOSVersion"`
}

func Current() Info {
	return New(commit, dirty == "true", buildTime, runtime.Version(), runtime.GOOS, runtime.GOARCH, macOSProductVersion())
}

func New(commit string, dirty bool, buildTime, goVersion, goos, goarch, macOSVersion string) Info {
	return Info{
		GitCommit:    valueOrUnknown(commit),
		BuildTime:    valueOrUnknown(buildTime),
		Dirty:        dirty,
		GoVersion:    valueOrUnknown(goVersion),
		GOOS:         valueOrUnknown(goos),
		GOARCH:       valueOrUnknown(goarch),
		MacOSVersion: valueOrUnknown(macOSVersion),
	}
}

func (i Info) Platform() string {
	return i.GOOS + "/" + i.GOARCH
}

func (i Info) BuildID() string {
	id := shortCommit(i.GitCommit)
	if i.Dirty {
		return id + "-dirty"
	}
	return id
}

func (i Info) LogString() string {
	return fmt.Sprintf(
		"build=%s commit=%s dirty=%t build_time=%s go=%s platform=%s macos=%s",
		i.BuildID(),
		i.GitCommit,
		i.Dirty,
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
