package claudecode

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const fingerprintBytes int64 = 64 * 1024

type File struct {
	Path     string
	Home     string
	HomeKey  string
	Project  string
	Main     bool
	Subagent bool
}

type FileMetadata struct {
	Identity string
	Size     int64
	MtimeNS  int64
	HeadHash string
	TailHash string
}

func ResolveHomes(configured []string) ([]string, error) {
	candidates := configured
	if len(candidates) == 0 {
		if value := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); value != "" {
			candidates = []string{value}
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("读取用户目录: %w", err)
			}
			candidates = []string{filepath.Join(home, ".claude")}
		}
	}

	seen := make(map[string]struct{}, len(candidates))
	homes := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		value := strings.TrimSpace(candidate)
		if value == "" {
			continue
		}
		absolute, err := filepath.Abs(value)
		if err != nil {
			return nil, fmt.Errorf("解析 Claude Code config 目录: %w", err)
		}
		clean := filepath.Clean(absolute)
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		homes = append(homes, clean)
	}
	sort.Strings(homes)
	return homes, nil
}

func Discover(homes []string) ([]File, error) {
	seen := make(map[string]File)
	for _, home := range homes {
		root := filepath.Join(home, "projects")
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			absolute, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, absolute)
			if err != nil {
				return err
			}
			parts := strings.Split(relative, string(filepath.Separator))
			mainTranscript := len(parts) == 2
			subagent := isSubagent(parts)
			if !mainTranscript && !subagent {
				return nil
			}
			seen[absolute] = File{
				Path:     absolute,
				Home:     home,
				HomeKey:  stableHash("claude-home|" + home),
				Project:  "unknown",
				Main:     mainTranscript,
				Subagent: subagent,
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("发现 Claude Code transcript 失败: %w", err)
		}
	}

	files := make([]File, 0, len(seen))
	for _, file := range seen {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func CheckpointKey(file File) string {
	return stableHash("claude-file|" + file.HomeKey + "|" + file.Path)
}

func SessionCount(files []File) int {
	count := 0
	for _, file := range files {
		if file.Main {
			count++
		}
	}
	return count
}

func Inspect(file File) (FileMetadata, error) {
	info, err := os.Stat(file.Path)
	if err != nil {
		return FileMetadata{}, fmt.Errorf("读取 Claude Code transcript 状态: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return FileMetadata{}, errors.New("当前系统不支持读取 Claude Code 文件标识")
	}
	headHash, err := hashRange(file.Path, 0, minInt64(fingerprintBytes, info.Size()))
	if err != nil {
		return FileMetadata{}, err
	}
	tailLength := minInt64(fingerprintBytes, info.Size())
	tailHash, err := hashRange(file.Path, info.Size()-tailLength, tailLength)
	if err != nil {
		return FileMetadata{}, err
	}
	return FileMetadata{
		Identity: fmt.Sprintf("%d:%d", stat.Dev, stat.Ino),
		Size:     info.Size(), MtimeNS: info.ModTime().UnixNano(),
		HeadHash: headHash, TailHash: tailHash,
	}, nil
}

func MatchesAppendPrefix(file File, previous FileMetadata) (bool, error) {
	current, err := Inspect(file)
	if err != nil {
		return false, err
	}
	if current.Identity != previous.Identity || current.Size <= previous.Size {
		return false, nil
	}
	return MatchesSnapshot(file, previous)
}

func MatchesSnapshot(file File, snapshot FileMetadata) (bool, error) {
	current, err := Inspect(file)
	if err != nil {
		return false, err
	}
	if current.Identity != snapshot.Identity || current.Size < snapshot.Size {
		return false, nil
	}
	headLength := minInt64(fingerprintBytes, snapshot.Size)
	headHash, err := hashRange(file.Path, 0, headLength)
	if err != nil {
		return false, err
	}
	tailLength := minInt64(fingerprintBytes, snapshot.Size)
	tailHash, err := hashRange(file.Path, snapshot.Size-tailLength, tailLength)
	if err != nil {
		return false, err
	}
	return headHash == snapshot.HeadHash && tailHash == snapshot.TailHash, nil
}

func isSubagent(parts []string) bool {
	for index, part := range parts {
		if part == "subagents" && index+1 < len(parts) && strings.HasPrefix(parts[index+1], "agent-") {
			return true
		}
	}
	return false
}

func hashRange(path string, offset, length int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开 Claude Code transcript: %w", err)
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", fmt.Errorf("定位 Claude Code transcript: %w", err)
	}
	hash := sha256.New()
	if _, err := io.CopyN(hash, file, length); err != nil {
		return "", fmt.Errorf("读取 Claude Code transcript 指纹: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func stableHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
