package menubar

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type View struct {
	Title      string
	Header     string
	Today      string
	SevenDays  string
	AllTime    string
	TopModel   string
	FiveHour   string
	SevenDay   string
	Status     string
	Refreshing bool
}

func BuildView(state *State, now time.Time, refreshing bool, statusOverride string) View {
	view := View{
		Title:      "Dora",
		Header:     "Dora",
		Today:      "今日：—",
		SevenDays:  "7 日：—",
		AllTime:    "全部：—",
		TopModel:   "模型：暂无数据",
		FiveHour:   "Codex 5 小时配额：暂无数据",
		SevenDay:   "Codex 7 日配额：暂无数据",
		Status:     "状态：正在连接本地服务",
		Refreshing: refreshing,
	}
	if state != nil {
		view.Title = compactTokens(state.Snapshot.Usage.TodayTokens)
		view.Today = tokenRow("今日", state.Snapshot.Usage.TodayTokens)
		view.SevenDays = tokenRow("7 日", state.Snapshot.Usage.SevenDayTokens)
		view.AllTime = tokenRow("全部", state.Snapshot.Usage.AllTimeTokens)
		if state.Snapshot.Usage.TopModel != "" {
			view.TopModel = "模型：" + state.Snapshot.Usage.TopModel
		}
		view.FiveHour = quotaRow("Codex 5 小时配额", "five_hour", state.Quota, now)
		view.SevenDay = quotaRow("Codex 7 日配额", "seven_day", state.Quota, now)
		view.Status = snapshotStatus(*state, now)
	}
	if refreshing {
		view.Status = "状态：正在刷新数据…"
	}
	if statusOverride != "" {
		view.Status = "状态：" + statusOverride
	}
	return view
}

func tokenRow(label string, tokens int64) string {
	return fmt.Sprintf("%s：%s tokens", label, compactTokens(tokens))
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
		return "状态：" + strings.Join(state.Snapshot.Errors, "；")
	}
	if state.Snapshot.Usage.LastScanAt == nil {
		return "状态：尚未扫描"
	}
	lastScan, err := time.Parse(time.RFC3339Nano, *state.Snapshot.Usage.LastScanAt)
	if err != nil {
		return "状态：已更新"
	}
	if state.Snapshot.Usage.Stale {
		return "状态：数据可能已过期 · " + lastScan.In(now.Location()).Format("15:04")
	}
	return "状态：已更新 · " + lastScan.In(now.Location()).Format("15:04")
}
