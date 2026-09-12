package workbuddyhooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const marker = "dora-workbuddy-hook-v1"

type hookSpec struct{ event, matcher string }

var observedEvents = []hookSpec{
	{"SessionStart", ""}, {"SessionEnd", ""}, {"UserPromptSubmit", ""},
	{"Notification", "^permission_prompt$"}, {"PostToolUse", ""}, {"PostToolUseFailure", ""}, {"SubagentStop", ""}, {"Stop", ""}, {"FinalStop", ""}, {"StopFailure", ""},
}

type Manager struct{ home, executable string }
type Status struct {
	Path, Executable string
	Installed        bool
	Missing          []string
}

func NewManager(home, executable string) (*Manager, error) {
	if !filepath.IsAbs(home) || !filepath.IsAbs(executable) {
		return nil, errors.New("WorkBuddy 配置和 Dora 程序必须使用绝对路径")
	}
	return &Manager{home: home, executable: executable}, nil
}
func (m *Manager) path() string { return filepath.Join(m.home, "settings.json") }
func (m *Manager) command() string {
	return "'" + strings.ReplaceAll(m.executable, "'", "'\"'\"'") + "' hooks emit workbuddy # " + marker
}
func (m *Manager) load() (map[string]json.RawMessage, map[string]json.RawMessage, os.FileMode, error) {
	root := map[string]json.RawMessage{}
	hooks := map[string]json.RawMessage{}
	mode := os.FileMode(0600)
	info, err := os.Lstat(m.path())
	if errors.Is(err, os.ErrNotExist) {
		return root, hooks, mode, nil
	}
	if err != nil {
		return nil, nil, 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, nil, 0, errors.New("WorkBuddy settings.json 必须是 1 MiB 以内的普通文件")
	}
	mode = info.Mode().Perm()
	data, err := os.ReadFile(m.path())
	if err != nil {
		return nil, nil, 0, err
	}
	if json.Unmarshal(data, &root) != nil || root == nil {
		return nil, nil, 0, errors.New("WorkBuddy settings.json 损坏，未修改原文件")
	}
	if raw, ok := root["hooks"]; ok {
		if json.Unmarshal(raw, &hooks) != nil || hooks == nil {
			return nil, nil, 0, errors.New("WorkBuddy hooks 配置格式无效，未修改原文件")
		}
	}
	return root, hooks, mode, nil
}
func (m *Manager) Install() (Status, error)   { return m.update(true) }
func (m *Manager) Uninstall() (Status, error) { return m.update(false) }
func (m *Manager) update(install bool) (Status, error) {
	root, hooks, mode, err := m.load()
	if err != nil {
		return Status{}, err
	}
	changed := false
	for event, raw := range hooks {
		eventChanged := false
		var groups []json.RawMessage
		if json.Unmarshal(raw, &groups) != nil {
			return Status{}, fmt.Errorf("WorkBuddy %s hooks 格式无效", event)
		}
		kept := groups[:0]
		for _, rawGroup := range groups {
			var group map[string]json.RawMessage
			if json.Unmarshal(rawGroup, &group) != nil || group == nil {
				return Status{}, errors.New("WorkBuddy hook group 格式无效")
			}
			var handlers []json.RawMessage
			if json.Unmarshal(group["hooks"], &handlers) != nil {
				return Status{}, fmt.Errorf("WorkBuddy %s handlers 格式无效", event)
			}
			rest := handlers[:0]
			removed := false
			for _, handler := range handlers {
				var command struct {
					Command string `json:"command"`
				}
				if json.Unmarshal(handler, &command) != nil {
					return Status{}, errors.New("WorkBuddy handler 格式无效")
				}
				if strings.HasSuffix(command.Command, " # "+marker) {
					changed = true
					eventChanged = true
					removed = true
					continue
				}
				rest = append(rest, handler)
			}
			if removed && len(rest) == 0 {
				continue
			}
			if removed {
				group["hooks"], _ = json.Marshal(rest)
				rawGroup, _ = json.Marshal(group)
			}
			kept = append(kept, rawGroup)
		}
		if !eventChanged {
			continue
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event], _ = json.Marshal(kept)
		}
	}
	if install {
		for _, spec := range observedEvents {
			var groups []json.RawMessage
			if raw, ok := hooks[spec.event]; ok {
				if err := json.Unmarshal(raw, &groups); err != nil {
					return Status{}, err
				}
			}
			group := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": m.command(), "timeout": 2}}}
			if spec.matcher != "" {
				group["matcher"] = spec.matcher
			}
			encoded, _ := json.Marshal(group)
			groups = append(groups, encoded)
			hooks[spec.event], _ = json.Marshal(groups)
		}
		changed = true
	}
	if !changed {
		return m.Status()
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"], _ = json.Marshal(hooks)
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return Status{}, err
	}
	data = append(data, '\n')
	old, err := os.ReadFile(m.path())
	if err == nil && bytes.Equal(old, data) {
		return m.Status()
	}
	if err := os.MkdirAll(m.home, 0700); err != nil {
		return Status{}, err
	}
	temp, err := os.CreateTemp(m.home, ".dora-hooks-*")
	if err != nil {
		return Status{}, err
	}
	defer os.Remove(temp.Name())
	if err = temp.Chmod(mode); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return Status{}, err
	}
	if closeErr != nil {
		return Status{}, closeErr
	}
	if err = os.Rename(temp.Name(), m.path()); err != nil {
		return Status{}, err
	}
	return m.Status()
}
func (m *Manager) Status() (Status, error) {
	status := Status{Path: m.path(), Executable: m.executable}
	_, hooks, _, err := m.load()
	if err != nil {
		return status, err
	}
	for _, spec := range observedEvents {
		var groups []struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type, Command string
				Timeout       int
			} `json:"hooks"`
		}
		raw, ok := hooks[spec.event]
		if ok && json.Unmarshal(raw, &groups) != nil {
			return status, errors.New("WorkBuddy hooks 配置格式无效")
		}
		found := false
		for _, group := range groups {
			if group.Matcher != spec.matcher {
				continue
			}
			for _, h := range group.Hooks {
				if h.Type == "command" && h.Command == m.command() && h.Timeout == 2 {
					found = true
				}
			}
		}
		if !found {
			status.Missing = append(status.Missing, spec.event)
		}
	}
	status.Installed = len(status.Missing) == 0
	return status, nil
}
