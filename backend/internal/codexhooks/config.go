package codexhooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const marker = "dora-attention-hook-v1"

var trustedHashAssignment = regexp.MustCompile(`^trusted_hash\s*=\s*["'](sha256:[0-9a-f]+)["'](?:\s*#.*)?$`)

var observedEvents = []hookSpec{
	{event: "SessionStart", keyLabel: "session_start", timeout: 2},
	{event: "SessionEnd", keyLabel: "session_end", timeout: 1},
	{event: "UserPromptSubmit", keyLabel: "user_prompt_submit", timeout: 2},
	{event: "PermissionRequest", keyLabel: "permission_request", timeout: 2},
	{event: "PreToolUse", keyLabel: "pre_tool_use", matcher: "request_user_input", timeout: 2},
	{event: "PostToolUse", keyLabel: "post_tool_use", timeout: 2},
	{event: "SubagentStop", keyLabel: "subagent_stop", timeout: 2},
	{event: "Stop", keyLabel: "stop", timeout: 2},
}

type hookSpec struct {
	event    string
	keyLabel string
	matcher  string
	timeout  int
}

type Manager struct {
	codexHome  string
	executable string
}

type Status struct {
	Path              string
	Executable        string
	Installed         bool
	Trust             string
	Broken            string
	ExecutableProblem string
	Missing           []string
}

func NewManager(codexHome, executable string) (*Manager, error) {
	if codexHome == "" || executable == "" {
		return nil, errors.New("Codex hooks 路径不能为空")
	}
	absExecutable, err := filepath.Abs(executable)
	if err != nil {
		return nil, fmt.Errorf("解析 Dora 可执行文件路径: %w", err)
	}
	return &Manager{codexHome: filepath.Clean(codexHome), executable: filepath.Clean(absExecutable)}, nil
}

func (m *Manager) Install() (Status, error) {
	root, hooks, mode, err := m.load()
	if err != nil {
		return Status{}, err
	}
	if hooks == nil {
		hooks = make(map[string]json.RawMessage)
	}
	if err := removeDoraHandlers(hooks); err != nil {
		return Status{}, err
	}
	command := m.command()
	for _, spec := range observedEvents {
		groups, err := decodeGroups(hooks[spec.event], spec.event)
		if err != nil {
			return Status{}, err
		}
		group := map[string]any{
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": command,
				"timeout": spec.timeout,
			}},
		}
		if spec.matcher != "" {
			group["matcher"] = spec.matcher
		}
		encoded, err := json.Marshal(group)
		if err != nil {
			return Status{}, fmt.Errorf("生成 Dora %s hook: %w", spec.event, err)
		}
		groups = append(groups, encoded)
		hooks[spec.event], err = json.Marshal(groups)
		if err != nil {
			return Status{}, fmt.Errorf("更新 Dora %s hook: %w", spec.event, err)
		}
	}
	if err := m.write(root, hooks, mode); err != nil {
		return Status{}, err
	}
	return m.Status()
}

func (m *Manager) Uninstall() (Status, error) {
	root, hooks, mode, err := m.load()
	if err != nil {
		return Status{}, err
	}
	if hooks == nil {
		return m.Status()
	}
	if err := removeDoraHandlers(hooks); err != nil {
		return Status{}, err
	}
	if err := m.write(root, hooks, mode); err != nil {
		return Status{}, err
	}
	return m.Status()
}

func (m *Manager) Status() (Status, error) {
	status := Status{Path: m.hooksPath(), Executable: m.executable, Trust: "not_installed"}
	_, hooks, _, err := m.load()
	if err != nil {
		status.Broken = err.Error()
		return status, nil
	}
	if hooks == nil {
		return status, nil
	}
	command := m.command()
	for _, spec := range observedEvents {
		groups, err := decodeGroups(hooks[spec.event], spec.event)
		if err != nil {
			status.Broken = err.Error()
			return status, nil
		}
		found := false
		for _, rawGroup := range groups {
			var group struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
					Timeout int    `json:"timeout"`
				} `json:"hooks"`
			}
			if err := json.Unmarshal(rawGroup, &group); err != nil {
				status.Broken = fmt.Sprintf("%s hook 配置无效", spec.event)
				return status, nil
			}
			for _, handler := range group.Hooks {
				if strings.Contains(handler.Command, marker) && handler.Type == "command" && handler.Command == command && handler.Timeout == spec.timeout && group.Matcher == spec.matcher {
					found = true
				}
			}
		}
		if !found {
			status.Missing = append(status.Missing, spec.event)
		}
	}
	status.Installed = len(status.Missing) == 0
	if status.Installed {
		status.Trust = m.trustStatus()
		info, err := os.Stat(m.executable)
		switch {
		case err != nil:
			status.ExecutableProblem = "Dora handler 路径不可用"
		case !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0:
			status.ExecutableProblem = "Dora handler 不是可执行文件"
		}
	}
	return status, nil
}

func (m *Manager) load() (map[string]json.RawMessage, map[string]json.RawMessage, os.FileMode, error) {
	root := make(map[string]json.RawMessage)
	data, err := os.ReadFile(m.hooksPath())
	if errors.Is(err, os.ErrNotExist) {
		return root, nil, 0o600, nil
	}
	if err != nil {
		return nil, nil, 0, fmt.Errorf("读取 Codex hooks 配置 %s: %w", m.hooksPath(), err)
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, nil, 0, fmt.Errorf("Codex hooks 配置 %s 已损坏，未做修改: %w", m.hooksPath(), err)
	}
	hooks := make(map[string]json.RawMessage)
	if raw := root["hooks"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, nil, 0, fmt.Errorf("Codex hooks 字段已损坏，未做修改: %w", err)
		}
	}
	info, err := os.Stat(m.hooksPath())
	if err != nil {
		return nil, nil, 0, fmt.Errorf("检查 Codex hooks 配置: %w", err)
	}
	return root, hooks, info.Mode().Perm(), nil
}

func (m *Manager) write(root, hooks map[string]json.RawMessage, mode os.FileMode) error {
	encodedHooks, err := json.Marshal(hooks)
	if err != nil {
		return fmt.Errorf("编码 Codex hooks: %w", err)
	}
	root["hooks"] = encodedHooks
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 Codex hooks 配置: %w", err)
	}
	data = append(data, '\n')
	if current, readErr := os.ReadFile(m.hooksPath()); readErr == nil && bytes.Equal(current, data) {
		return nil
	}
	if err := os.MkdirAll(m.codexHome, 0o700); err != nil {
		return fmt.Errorf("创建 Codex 配置目录: %w", err)
	}
	temporary, err := os.CreateTemp(m.codexHome, ".dora-hooks-*.json")
	if err != nil {
		return fmt.Errorf("创建 Codex hooks 临时文件: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if mode == 0 {
		mode = 0o600
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return fmt.Errorf("设置 Codex hooks 文件权限: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("写入 Codex hooks 配置: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("同步 Codex hooks 配置: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭 Codex hooks 临时文件: %w", err)
	}
	if err := os.Rename(temporaryPath, m.hooksPath()); err != nil {
		return fmt.Errorf("原子更新 Codex hooks 配置: %w", err)
	}
	return nil
}

func removeDoraHandlers(hooks map[string]json.RawMessage) error {
	for event, raw := range hooks {
		groups, err := decodeGroups(raw, event)
		if err != nil {
			return err
		}
		keptGroups := make([]json.RawMessage, 0, len(groups))
		for _, rawGroup := range groups {
			var group map[string]json.RawMessage
			if err := json.Unmarshal(rawGroup, &group); err != nil {
				return fmt.Errorf("%s hook group 已损坏，未做修改: %w", event, err)
			}
			var handlers []json.RawMessage
			if rawHandlers := group["hooks"]; len(rawHandlers) != 0 {
				if err := json.Unmarshal(rawHandlers, &handlers); err != nil {
					return fmt.Errorf("%s hook handler 已损坏，未做修改: %w", event, err)
				}
			}
			keptHandlers := make([]json.RawMessage, 0, len(handlers))
			for _, rawHandler := range handlers {
				var handler struct {
					Command string `json:"command"`
				}
				if err := json.Unmarshal(rawHandler, &handler); err != nil {
					return fmt.Errorf("%s hook command 已损坏，未做修改: %w", event, err)
				}
				if !strings.Contains(handler.Command, marker) {
					keptHandlers = append(keptHandlers, rawHandler)
				}
			}
			if len(keptHandlers) == 0 && len(handlers) > 0 {
				continue
			}
			if len(handlers) > 0 {
				group["hooks"], err = json.Marshal(keptHandlers)
				if err != nil {
					return fmt.Errorf("更新 %s hook handlers: %w", event, err)
				}
			}
			encoded, err := json.Marshal(group)
			if err != nil {
				return fmt.Errorf("更新 %s hook group: %w", event, err)
			}
			keptGroups = append(keptGroups, encoded)
		}
		if len(keptGroups) == 0 {
			delete(hooks, event)
			continue
		}
		hooks[event], err = json.Marshal(keptGroups)
		if err != nil {
			return fmt.Errorf("更新 %s hooks: %w", event, err)
		}
	}
	return nil
}

func decodeGroups(raw json.RawMessage, event string) ([]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var groups []json.RawMessage
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, fmt.Errorf("Codex %s hooks 已损坏，未做修改: %w", event, err)
	}
	return groups, nil
}

func (m *Manager) command() string {
	return shellQuote(m.executable) + " hooks emit codex # " + marker
}

func (m *Manager) hooksPath() string {
	return filepath.Join(m.codexHome, "hooks.json")
}

func (m *Manager) trustStatus() string {
	config, err := os.ReadFile(filepath.Join(m.codexHome, "config.toml"))
	if err != nil {
		return "untrusted"
	}
	assignments := make(map[string]struct{})
	for _, line := range strings.Split(string(config), "\n") {
		match := trustedHashAssignment.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) == 2 {
			assignments[match[1]] = struct{}{}
		}
	}
	trusted := 0
	for _, spec := range observedEvents {
		hash := normalizedHookHash(spec, m.command())
		if _, ok := assignments[hash]; ok {
			trusted++
		}
	}
	switch {
	case trusted == len(observedEvents):
		return "trusted"
	case trusted > 0:
		return "partial"
	default:
		return "untrusted"
	}
}

func normalizedHookHash(spec hookSpec, command string) string {
	handler := map[string]any{
		"async":   false,
		"command": command,
		"timeout": spec.timeout,
		"type":    "command",
	}
	group := map[string]any{"hooks": []any{handler}}
	if spec.matcher != "" {
		group["matcher"] = spec.matcher
	}
	identity := map[string]any{"event_name": spec.keyLabel}
	for key, value := range group {
		identity[key] = value
	}
	data, _ := json.Marshal(identity)
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
