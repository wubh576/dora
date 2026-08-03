package menubar

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type ScreenMetrics struct {
	Frame            Rect
	Visible          Rect
	SafeTop          float64
	MenuBarThickness float64
}

type PanelLayout struct {
	Frame           Rect    `json:"frame"`
	Scrollable      bool    `json:"scrollable"`
	SessionViewport float64 `json:"sessionViewport"`
}

type View struct {
	Expanded           bool         `json:"expanded"`
	Mode               string       `json:"mode"`
	Layout             PanelLayout  `json:"layout"`
	AnimateFrame       bool         `json:"animateFrame"`
	CompactSummary     string       `json:"compactSummary"`
	CompactTokens      string       `json:"compactTokens"`
	WaitingCount       int          `json:"waitingCount"`
	RunningCount       int          `json:"runningCount"`
	Today              string       `json:"today"`
	SevenDays          string       `json:"sevenDays"`
	AllTime            string       `json:"allTime"`
	FiveHour           string       `json:"fiveHour"`
	SevenDay           string       `json:"sevenDay"`
	Status             string       `json:"status"`
	OperationStatus    string       `json:"operationStatus,omitempty"`
	OperationError     bool         `json:"operationError,omitempty"`
	Refreshing         bool         `json:"refreshing"`
	HighlightSessionID int64        `json:"highlightSessionId,omitempty"`
	HighlightRequestID int64        `json:"highlightRequestId,omitempty"`
	Sessions           []SessionRow `json:"sessions"`
}

type SessionRow struct {
	ID         int64  `json:"id"`
	State      string `json:"state"`
	Title      string `json:"title"`
	Subtitle   string `json:"subtitle"`
	Meta       string `json:"meta"`
	Highlight  bool   `json:"highlight"`
	Jumpable   bool   `json:"jumpable"`
	JumpReason string `json:"jumpReason,omitempty"`
}

func BuildView(state *State, machine MachineState, screen ScreenMetrics, now time.Time, refreshing bool, statusOverride string) View {
	view := View{
		Expanded: machine.Mode != ModeCompact, Mode: string(machine.Mode),
		CompactSummary: "Dora", CompactTokens: "今日 token —", Today: "今日 —", SevenDays: "7 日 —", AllTime: "全部 —",
		FiveHour: "Codex 5 小时配额：暂无数据", SevenDay: "Codex 7 日配额：暂无数据",
		Status: "正在连接本地服务", Refreshing: refreshing,
		HighlightSessionID: machine.HighlightSessionID,
		HighlightRequestID: machine.HighlightRequestID,
	}
	if state != nil {
		view.WaitingCount = state.Runtime.WaitingCount
		view.RunningCount = state.Runtime.RunningCount
		view.CompactTokens = "今日 token " + compactTokens(state.Snapshot.Usage.TodayTokens)
		view.Today = tokenRow("今日", state.Snapshot.Usage.TodayTokens)
		view.SevenDays = tokenRow("7 日", state.Snapshot.Usage.SevenDayTokens)
		view.AllTime = tokenRow("全部", state.Snapshot.Usage.AllTimeTokens)
		view.FiveHour = quotaRow("Codex 5 小时配额", "five_hour", state.Quota, now)
		view.SevenDay = quotaRow("Codex 7 日配额", "seven_day", state.Quota, now)
		view.Status = snapshotStatus(*state, now)
		view.Sessions = sessionRows(state.Runtime, machine.HighlightSessionID, now)
	}
	if refreshing {
		view.Status = "正在刷新数据…"
	}
	if statusOverride != "" {
		view.Status = statusOverride
		view.OperationStatus = statusOverride
		view.OperationError = statusIsError(statusOverride)
	}
	view.Layout = CalculateLayout(screen, view.Expanded, len(view.Sessions), view.RunningCount, view.WaitingCount)
	return view
}

func statusIsError(status string) bool {
	for _, marker := range []string{"失败", "无法", "未配置", "拒绝", "已经结束", "缺少", "不支持"} {
		if strings.Contains(status, marker) {
			return true
		}
	}
	return false
}

func CalculateLayout(screen ScreenMetrics, expanded bool, sessions, running, waiting int) PanelLayout {
	visible := screen.Visible
	if visible.Width <= 0 || visible.Height <= 0 {
		visible = screen.Frame
	}
	width, height := 360.0, compactHeight(screen)
	if !expanded {
		width = 360
	} else {
		width = min(760, max(600, visible.Width-32))
		natural := 244.0 + float64(sessions)*56
		height = min(520, min(visible.Height*0.65, natural))
		height = max(244, height)
	}
	width = min(width, max(1, visible.Width-16))
	height = min(height, max(1, visible.Height-16))
	top := screen.Frame.Y + screen.Frame.Height
	frame := Rect{X: screen.Frame.X + (screen.Frame.Width-width)/2, Y: top - height, Width: width, Height: height}
	viewport := 0.0
	if expanded {
		viewport = max(0, height-176)
	}
	return PanelLayout{Frame: frame, Scrollable: expanded && float64(sessions)*56 > viewport, SessionViewport: viewport}
}

func compactHeight(screen ScreenMetrics) float64 {
	// visibleFrame 的顶边差值是当前屏幕实际被菜单栏占用的高度。
	if height := screen.Frame.Y + screen.Frame.Height - (screen.Visible.Y + screen.Visible.Height); height > 0 {
		return height
	}
	if screen.SafeTop > 0 {
		return screen.SafeTop
	}
	return max(1, screen.MenuBarThickness)
}

func sessionRows(runtime RuntimeState, highlight int64, now time.Time) []SessionRow {
	rows := make([]SessionRow, 0, len(runtime.Sessions))
	for _, session := range runtime.Sessions {
		source := sourceLabel(session)
		name := session.SessionName
		if name == "" {
			name = "未命名会话"
		}
		title := "Codex · " + name
		subtitle := session.PromptPreview
		if session.State == "waiting" && session.Summary != "" {
			if subtitle == "" {
				subtitle = session.Summary
			} else {
				subtitle = session.Summary + " · " + subtitle
			}
		} else if subtitle == "" {
			subtitle = "Codex 正在运行"
		}
		meta := source
		if session.State == "waiting" {
			meta += " · " + waitLabel(session.WaitSeconds)
			if session.RequestCount > 1 {
				meta += fmt.Sprintf(" · %d 个请求", session.RequestCount)
			}
		} else {
			meta += " · " + activityLabel(session.LastSeenAt, now)
		}
		rows = append(rows, SessionRow{
			ID: session.ID, State: session.State, Title: title, Subtitle: subtitle,
			Meta: meta, Highlight: session.ID == highlight,
			Jumpable: session.Jumpable, JumpReason: session.JumpReason,
		})
	}
	return rows
}

func activityLabel(value string, now time.Time) string {
	lastSeen, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "运行中"
	}
	seconds := max(0, int64(now.Sub(lastSeen).Seconds()))
	switch {
	case seconds < 5:
		return "刚刚"
	case seconds < 60:
		return fmt.Sprintf("%d 秒前", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%d 分钟前", seconds/60)
	default:
		return fmt.Sprintf("%d 小时前", seconds/3600)
	}
}

func sourceLabel(session RuntimeSession) string {
	switch session.Surface {
	case "codex_app":
		return "Codex App"
	case "codex_cli":
		switch session.TerminalKind {
		case "iterm2":
			return "iTerm2"
		case "terminal":
			return "Terminal"
		default:
			return "CLI"
		}
	default:
		return "Codex"
	}
}

func waitLabel(seconds int64) string {
	seconds = max(0, seconds)
	switch {
	case seconds < 60:
		return fmt.Sprintf("等待 %d 秒", seconds)
	case seconds < 3600:
		return fmt.Sprintf("等待 %d 分钟", seconds/60)
	default:
		return fmt.Sprintf("等待 %d 小时 %d 分钟", seconds/3600, seconds%3600/60)
	}
}

func tokenRow(label string, tokens int64) string {
	return fmt.Sprintf("%s %s tokens", label, compactTokens(tokens))
}

func compactTokens(value int64) string {
	if value < 1_000 {
		return fmt.Sprintf("%d", value)
	}
	units := []struct {
		value float64
		label string
	}{{1e3, "K"}, {1e6, "M"}, {1e9, "B"}, {1e12, "T"}}
	unitIndex := 0
	for unitIndex+1 < len(units) && float64(value) >= units[unitIndex+1].value {
		unitIndex++
	}
	rounded := math.Round(float64(value)/units[unitIndex].value*10) / 10
	if rounded >= 1_000 && unitIndex+1 < len(units) {
		unitIndex++
		rounded = math.Round(float64(value)/units[unitIndex].value*10) / 10
	}
	if rounded == math.Round(rounded) {
		return fmt.Sprintf("%.0f%s", rounded, units[unitIndex].label)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", rounded), ".0") + units[unitIndex].label
}

func quotaRow(label, window string, quota QuotaState, now time.Time) string {
	if !quota.Enabled {
		return label + "：已关闭"
	}
	for _, item := range quota.Items {
		if item.WindowKey != window {
			continue
		}
		value := fmt.Sprintf("%s：剩余 %.0f%%", label, item.RemainingPercent)
		if item.ResetsAt != nil {
			if resetAt, err := time.Parse(time.RFC3339Nano, *item.ResetsAt); err == nil {
				value += " · " + resetLabel(resetAt.In(now.Location()), now)
			}
		}
		if item.SourceState == "stale" {
			value += "（数据过期）"
		}
		return value
	}
	switch quota.Status {
	case "not_configured":
		return label + "：未登录"
	case "unsupported":
		return label + "：当前登录方式不支持"
	case "error":
		return label + "：读取失败"
	default:
		return label + "：暂无数据"
	}
}

func resetLabel(resetAt, now time.Time) string {
	if resetAt.Before(now) {
		return "等待更新"
	}
	if resetAt.Sub(now) < 24*time.Hour {
		return resetAt.Format("15:04") + " 重置"
	}
	return resetAt.Format("1月2日 15:04") + " 重置"
}

func snapshotStatus(state State, now time.Time) string {
	if len(state.Snapshot.Errors) > 0 {
		return strings.Join(state.Snapshot.Errors, "；")
	}
	if state.Snapshot.Usage.LastScanAt == nil {
		return "尚未扫描"
	}
	lastScan, err := time.Parse(time.RFC3339Nano, *state.Snapshot.Usage.LastScanAt)
	if err != nil {
		return "已更新"
	}
	if state.Snapshot.Usage.Stale {
		return "数据可能已过期 · " + lastScan.In(now.Location()).Format("15:04")
	}
	return "已更新 · " + lastScan.In(now.Location()).Format("15:04")
}
