package launchagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"
)

const (
	DefaultLogMaxBytes      int64 = 200 * 1024 * 1024
	DefaultLogCheckInterval       = 10 * time.Minute
)

type RotationLogger interface {
	Printf(string, ...any)
}

type LogRotationConfig struct {
	Files         []string
	MaxBytes      int64
	CheckInterval time.Duration
	Logger        RotationLogger
}

// LogRotator 复制当前内容后清空活动文件，避免替换 launchd 已打开的 inode。
type LogRotator struct {
	files         []string
	maxBytes      int64
	checkInterval time.Duration
	logger        RotationLogger
}

func NewLogRotator(config LogRotationConfig) *LogRotator {
	if config.MaxBytes <= 0 {
		config.MaxBytes = DefaultLogMaxBytes
	}
	if config.CheckInterval <= 0 {
		config.CheckInterval = DefaultLogCheckInterval
	}
	if config.Logger == nil {
		config.Logger = log.Default()
	}
	return &LogRotator{
		files:         append([]string(nil), config.Files...),
		maxBytes:      config.MaxBytes,
		checkInterval: config.CheckInterval,
		logger:        config.Logger,
	}
}

func (r *LogRotator) Check() {
	for _, path := range r.files {
		rotated, err := rotateLog(path, r.maxBytes)
		if err != nil {
			r.logger.Printf("Dora 日志轮转失败: path=%s error=%s；将在下次检查重试", path, oneLineRotationError(err))
			continue
		}
		if rotated {
			r.logger.Printf("Dora 日志已轮转: path=%s backup=%s threshold=%d", path, path+".1", r.maxBytes)
		}
	}
}

func (r *LogRotator) Run(ctx context.Context) {
	ticker := time.NewTicker(r.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Check()
		}
	}
}

func rotateLog(path string, maxBytes int64) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查活动日志: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("活动日志不是普通文件")
	}
	if info.Size() < maxBytes {
		return false, nil
	}

	active, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("打开活动日志: %w", err)
	}
	defer active.Close()

	temporaryPath := path + ".1.tmp"
	if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("清理轮转临时文件: %w", err)
	}
	temporary, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("创建轮转临时文件: %w", err)
	}
	defer os.Remove(temporaryPath)
	if _, err := io.CopyN(temporary, active, info.Size()); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("备份活动日志: %w", err)
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("设置备份日志权限: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("同步备份日志: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("关闭备份日志: %w", err)
	}
	if err := os.Rename(temporaryPath, path+".1"); err != nil {
		return false, fmt.Errorf("替换备份日志: %w", err)
	}
	if err := os.Truncate(path, 0); err != nil {
		return false, fmt.Errorf("清空活动日志: %w", err)
	}
	return true, nil
}

func oneLineRotationError(err error) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(err.Error())
}
