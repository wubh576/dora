package launchagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/wubh576/dora/backend/internal/buildinfo"
)

type ManagedFile interface {
	io.Reader
	io.Writer
	io.Closer
	Sync() error
	Chmod(os.FileMode) error
}

type FileSystem interface {
	MkdirAll(string, os.FileMode) error
	Open(string) (ManagedFile, error)
	OpenFile(string, int, os.FileMode) (ManagedFile, error)
	Rename(string, string) error
	Remove(string) error
	Stat(string) (os.FileInfo, error)
}

type OSFileSystem struct{}

func (OSFileSystem) MkdirAll(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }
func (OSFileSystem) Open(path string) (ManagedFile, error)        { return os.Open(path) }
func (OSFileSystem) OpenFile(path string, flag int, mode os.FileMode) (ManagedFile, error) {
	return os.OpenFile(path, flag, mode)
}
func (OSFileSystem) Rename(oldPath, newPath string) error  { return os.Rename(oldPath, newPath) }
func (OSFileSystem) Remove(path string) error              { return os.Remove(path) }
func (OSFileSystem) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }

type Runner interface {
	Run(context.Context, string, ...string) (string, error)
}

type CommandRunner struct{}

func (CommandRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(output), err
}

type HealthChecker interface {
	Check(context.Context, string) (buildinfo.Info, error)
}

type PortChecker interface {
	Available(context.Context, string) error
}

type TCPPortChecker struct{}

func (TCPPortChecker) Available(ctx context.Context, address string) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return listener.Close()
}

type HTTPHealthChecker struct {
	Client *http.Client
}

func (checker HTTPHealthChecker) Check(ctx context.Context, baseURL string) (buildinfo.Info, error) {
	client := checker.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/health", nil)
	if err != nil {
		return buildinfo.Info{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return buildinfo.Info{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return buildinfo.Info{}, fmt.Errorf("health 返回 HTTP %d", response.StatusCode)
	}
	var health struct {
		Backend   bool           `json:"backend"`
		SQLite    bool           `json:"sqlite"`
		BuildInfo buildinfo.Info `json:"buildInfo"`
	}
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		return buildinfo.Info{}, fmt.Errorf("解析 health: %w", err)
	}
	if !health.Backend || !health.SQLite {
		return buildinfo.Info{}, fmt.Errorf("health 未就绪: backend=%t sqlite=%t", health.Backend, health.SQLite)
	}
	return health.BuildInfo, nil
}

func atomicCopy(files FileSystem, source, temporary, target string, mode os.FileMode) error {
	input, err := files.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	return writeTemporary(files, temporary, target, mode, func(output io.Writer) error {
		_, err := io.Copy(output, input)
		return err
	})
}

func atomicWrite(files FileSystem, temporary, target string, data []byte, mode os.FileMode) error {
	return writeTemporary(files, temporary, target, mode, func(output io.Writer) error {
		_, err := output.Write(data)
		return err
	})
}

func writeTemporary(files FileSystem, temporary, target string, mode os.FileMode, write func(io.Writer) error) (result error) {
	if err := removeIfExists(files, temporary); err != nil {
		return err
	}
	output, err := files.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		_ = output.Close()
		if result != nil {
			_ = files.Remove(temporary)
		}
	}()
	if err := write(output); err != nil {
		return err
	}
	if err := output.Chmod(mode); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := files.Rename(temporary, target); err != nil {
		return err
	}
	return nil
}
