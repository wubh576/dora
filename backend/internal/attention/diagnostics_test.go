package attention

import (
	"strings"
	"testing"
)

func TestSessionLabelIsStableAndDoesNotExposeSessionID(t *testing.T) {
	sessionID := "019-private-session"
	first := SessionLabel(sessionID)
	if first != SessionLabel(sessionID) || len(first) != 12 || strings.Contains(first, sessionID) {
		t.Fatalf("session 日志标识不安全或不稳定: %q", first)
	}
	if first == SessionLabel(sessionID+"-other") {
		t.Fatal("不同 session 生成相同测试标识")
	}
}
