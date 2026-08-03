package menubar

import (
	"strings"
	"testing"
	"time"
)

func TestBuildViewShowsWaitingBeforeRunningAndSafePreview(t *testing.T) {
	state := &State{
		Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: 1_250_000, SevenDayTokens: 2_000_000, ThirtyDayTokens: 2_500_000, AllTimeTokens: 3_000_000}},
		Runtime: RuntimeState{WaitingCount: 1, RunningCount: 1, Sessions: []RuntimeSession{
			{ID: 1, State: "waiting", SessionName: "dora", Surface: "codex_app", PromptPreview: "确认命令", Summary: "命令等待授权", WaitSeconds: 90, RequestCount: 2, Jumpable: true},
			{ID: 2, State: "running", SessionName: "backend", Surface: "codex_cli", TerminalKind: "iterm2", PromptPreview: "实现 API", JumpReason: "Codex CLI 会话缺少精确 TTY"},
		}},
	}
	view := BuildView(state, MachineState{Mode: ModeAttention, HighlightSessionID: 1}, testScreen(), time.Now(), false, "")
	if !view.Expanded || view.CompactSummary != "Dora" || view.CompactStatus != "1 等待 · 1 运行" {
		t.Fatalf("compact 内容错误: %+v", view)
	}
	if view.Today != "今日 1.3M tokens" || view.SevenDays != "近 7 日 2M tokens" ||
		view.ThirtyDays != "近 30 日 2.5M tokens" || view.AllTime != "全部 3M tokens" {
		t.Fatalf("token 时间范围错误: %+v", view)
	}
	if len(view.Sessions) != 2 || view.Sessions[0].State != "waiting" || !view.Sessions[0].Highlight || !view.Sessions[0].Jumpable ||
		view.Sessions[1].Meta != "iTerm2 · 运行中" || view.Sessions[1].Jumpable || view.Sessions[1].JumpReason == "" {
		t.Fatalf("session 行错误: %+v", view.Sessions)
	}
	serialized := view.Sessions[0].Title + view.Sessions[0].Subtitle + view.Sessions[0].Meta
	if strings.Contains(serialized, "session-") || strings.Contains(serialized, "/Users/") {
		t.Fatalf("视图泄露私有定位字段: %s", serialized)
	}
}

func TestSessionRowsShowWaitingReasonAndUpdatedRunningActivity(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	runtime := RuntimeState{Sessions: []RuntimeSession{
		{ID: 1, State: "waiting", Surface: "codex_app", SessionName: "dora", PromptPreview: "继续执行", Summary: "Codex 等待授权", WaitSeconds: 9, RequestCount: 1},
		{ID: 2, State: "running", Surface: "codex_cli", TerminalKind: "iterm2", SessionName: "backend", LastSeenAt: now.Add(-8 * time.Second).Format(time.RFC3339Nano)},
	}}
	rows := sessionRows(runtime, 0, now)
	if !strings.Contains(rows[0].Subtitle, "Codex 等待授权") || rows[1].Meta != "iTerm2 · 8 秒前" || rows[1].Title != "Codex · backend" {
		t.Fatalf("会话状态 meta 错误: %+v", rows)
	}
	rows = sessionRows(runtime, 0, now.Add(55*time.Second))
	if rows[1].Meta != "iTerm2 · 1 分钟前" {
		t.Fatalf("最近活动时间未随刷新更新: %q", rows[1].Meta)
	}
}

func TestCalculateLayoutCentersAndBoundsPanels(t *testing.T) {
	tests := []struct {
		name     string
		screen   ScreenMetrics
		expanded bool
		sessions int
	}{
		{name: "刘海屏紧贴顶部", screen: testScreen(), expanded: false},
		{name: "普通屏紧贴顶部", screen: ScreenMetrics{Frame: Rect{X: 100, Y: 20, Width: 1920, Height: 1080}, Visible: Rect{X: 100, Y: 20, Width: 1920, Height: 1056}}, expanded: true, sessions: 2},
		{name: "小屏收窄并滚动", screen: ScreenMetrics{Frame: Rect{Width: 560, Height: 700}, Visible: Rect{Width: 560, Height: 675}}, expanded: true, sessions: 20},
		{name: "极矮屏保持边界", screen: ScreenMetrics{Frame: Rect{Width: 800, Height: 240}, Visible: Rect{Width: 800, Height: 220}}, expanded: true, sessions: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			layout := CalculateLayout(test.screen, test.expanded, test.sessions, 3, 2)
			visible := test.screen.Visible
			wantCenter := test.screen.Frame.X + test.screen.Frame.Width/2
			gotCenter := layout.Frame.X + layout.Frame.Width/2
			if gotCenter != wantCenter {
				t.Fatalf("面板未居中: got %.1f want %.1f", gotCenter, wantCenter)
			}
			if layout.Frame.X < visible.X || layout.Frame.X+layout.Frame.Width > visible.X+visible.Width {
				t.Fatalf("面板横向越界: %+v in %+v", layout.Frame, visible)
			}
			if layout.Frame.Height > 520 || layout.Frame.Y < visible.Y || layout.Frame.Y+layout.Frame.Height != test.screen.Frame.Y+test.screen.Frame.Height {
				t.Fatalf("面板高度或底部越界: %+v", layout.Frame)
			}
			if test.sessions == 20 && !layout.Scrollable {
				t.Fatal("多会话没有启用中部滚动")
			}
		})
	}
}

func TestCalculateLayoutAlwaysAnchorsTopAndKeepsCompactCopyFixed(t *testing.T) {
	for _, safeTop := range []float64{0, 38} {
		screen := ScreenMetrics{
			Frame:   Rect{X: 100, Y: 20, Width: 1512, Height: 982},
			Visible: Rect{X: 100, Y: 20, Width: 1512, Height: 947}, SafeTop: safeTop,
		}
		for _, expanded := range []bool{false, true} {
			layout := CalculateLayout(screen, expanded, 8, 99, 77)
			if got, want := layout.Frame.Y+layout.Frame.Height, screen.Frame.Y+screen.Frame.Height; got != want {
				t.Fatalf("SafeTop %.0f expanded=%t maxY=%.1f, want %.1f", safeTop, expanded, got, want)
			}
		}
		first := CalculateLayout(screen, false, 0, 0, 0)
		second := CalculateLayout(screen, false, 20, 99, 77)
		if first.Frame.Width != 360 || second.Frame.Width != first.Frame.Width || first.Frame.Height != 35 {
			t.Fatalf("compact 宽度随状态变化: first=%+v second=%+v", first.Frame, second.Frame)
		}
	}
	view := BuildView(&State{Snapshot: Snapshot{Usage: SnapshotUsage{TodayTokens: 24_000}}, Runtime: RuntimeState{RunningCount: 3, WaitingCount: 2}},
		MachineState{Mode: ModeCompact}, testScreen(), time.Now(), false, "连接失败")
	if view.CompactSummary != "Dora" || view.CompactStatus != "2 等待 · 3 运行" {
		t.Fatalf("compact 固定文案被状态替换: %+v", view)
	}
}

func TestCompactHeightFollowsCurrentMenuBarBounds(t *testing.T) {
	tests := []struct {
		name   string
		screen ScreenMetrics
		want   float64
	}{
		{
			name: "可见菜单栏使用实际上下边界",
			screen: ScreenMetrics{
				Frame: Rect{Width: 1512, Height: 982}, Visible: Rect{Width: 1512, Height: 944},
				SafeTop: 32, MenuBarThickness: 22,
			},
			want: 38,
		},
		{
			name: "自动隐藏菜单栏时使用安全区",
			screen: ScreenMetrics{
				Frame: Rect{Width: 1512, Height: 982}, Visible: Rect{Width: 1512, Height: 982},
				SafeTop: 32, MenuBarThickness: 22,
			},
			want: 32,
		},
		{
			name: "普通屏自动隐藏时使用系统菜单栏厚度",
			screen: ScreenMetrics{
				Frame: Rect{Width: 1920, Height: 1080}, Visible: Rect{Width: 1920, Height: 1080},
				MenuBarThickness: 22,
			},
			want: 22,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			layout := CalculateLayout(test.screen, false, 0, 0, 0)
			if layout.Frame.Height != test.want {
				t.Fatalf("compact 高度 = %.1f, want %.1f", layout.Frame.Height, test.want)
			}
			if layout.Frame.Y+layout.Frame.Height != test.screen.Frame.Y+test.screen.Frame.Height {
				t.Fatalf("compact 未贴合屏幕顶边: %+v", layout.Frame)
			}
		})
	}
}

func TestCompactTokensUsesReadableUnits(t *testing.T) {
	for value, want := range map[int64]string{999: "999", 1_000: "1K", 1_250_000: "1.3M", 1_000_000_000: "1B", 2_000_000_000_000: "2T"} {
		if got := compactTokens(value); got != want {
			t.Fatalf("compactTokens(%d) = %q, want %q", value, got, want)
		}
	}
}

func TestBuildViewDistinguishesSuccessAndErrorStatus(t *testing.T) {
	success := BuildView(nil, MachineState{Mode: ModeCompact}, testScreen(), time.Now(), false, "刷新完成")
	if success.OperationError {
		t.Fatalf("成功状态被标成错误: %+v", success)
	}
	for _, message := range []string{"实时状态连接失败", "Codex CLI 会话缺少精确 TTY", "不支持该 Codex 会话来源"} {
		failure := BuildView(nil, MachineState{Mode: ModeCompact}, testScreen(), time.Now(), false, message)
		if !failure.OperationError {
			t.Fatalf("错误状态未标红: message=%q view=%+v", message, failure)
		}
	}
}

func testScreen() ScreenMetrics {
	return ScreenMetrics{
		Frame: Rect{Width: 1512, Height: 982}, Visible: Rect{Width: 1512, Height: 947},
		SafeTop: 32, MenuBarThickness: 22,
	}
}
