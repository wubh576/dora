package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wubh576/dora/backend/internal/httpapi"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("Dora 启动失败: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("用法: dora serve [--addr 127.0.0.1:8080] [--db 数据库路径]")
	}

	defaultDBPath, err := databasePath()
	if err != nil {
		return err
	}

	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := flags.String("addr", "127.0.0.1:8080", "HTTP 监听地址")
	dbPath := flags.String("db", defaultDBPath, "SQLite 数据库路径")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if err := validateLoopbackAddress(*addr); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := dorasqlite.Open(ctx, *dbPath)
	if err != nil {
		return fmt.Errorf("初始化 SQLite: %w", err)
	}
	defer store.Close()

	initializedAt, err := store.InitializedAt(ctx)
	if err != nil {
		return fmt.Errorf("读取 Dora 初始化状态: %w", err)
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.NewHandler(store),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Dora 后端已启动: http://%s（初始化时间 %s）", *addr, initializedAt.Format(time.RFC3339))
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("运行 HTTP 服务: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("关闭 HTTP 服务: %w", err)
		}
		return nil
	}
}

func databasePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("读取用户应用目录: %w", err)
	}
	return filepath.Join(configDir, "Dora", "dora.db"), nil
}

func validateLoopbackAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("解析 HTTP 监听地址 %q: %w", addr, err)
	}
	if host != "127.0.0.1" {
		return fmt.Errorf("HTTP 只能监听 127.0.0.1，当前地址为 %q", addr)
	}
	return nil
}
