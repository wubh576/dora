package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

type Values struct {
	CodexQuotaConsent bool `json:"codexQuotaConsent"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func New(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Load() (Values, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) Save(values Values) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建设置目录: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("读取设置目录: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return fmt.Errorf("创建临时设置文件: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("设置临时文件权限: %w", err)
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(values); err != nil {
		temp.Close()
		return fmt.Errorf("写入设置: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("同步设置: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("关闭设置文件: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("替换设置文件: %w", err)
	}
	return nil
}

func (s *Store) load() (Values, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Values{}, nil
	}
	if err != nil {
		return Values{}, fmt.Errorf("打开设置: %w", err)
	}
	defer file.Close()

	var values Values
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&values); err != nil {
		return Values{}, fmt.Errorf("解析设置: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Values{}, errors.New("设置文件只能包含一个 JSON 对象")
	}
	return values, nil
}
