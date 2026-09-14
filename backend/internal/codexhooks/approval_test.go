package codexhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wubh576/dora/backend/internal/approval"
	"github.com/wubh576/dora/backend/internal/domain"
	"github.com/wubh576/dora/backend/internal/httpapi"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

type approvalHarness struct {
	emitter *Emitter
	server  *httptest.Server
	dbPath  string
}

func newApprovalHarness(t *testing.T) *approvalHarness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dora.db")
	store, err := dorasqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv := httptest.NewUnstartedServer(nil)
	base := "http://" + srv.Listener.Addr().String()
	srv.Config.Handler = httpapi.NewHandler(store, httpapi.Options{ControlToken: "approval-test", AllowedOrigins: []string{base}})
	srv.Start()
	t.Cleanup(srv.Close)
	e := NewEmitter()
	e.endpoint = base + "/api/v1/hooks/codex"
	e.detector = fixedDetector{surface: Surface{Name: domain.CodexSurfaceApp}}
	return &approvalHarness{e, srv, path}
}

func (h *approvalHarness) request(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	encoded, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, h.server.URL+path, bytes.NewReader(encoded))
	req.Header.Set("Origin", h.server.URL)
	req.Header.Set("X-Dora-Control-Token", "approval-test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func (h *approvalHarness) list(t *testing.T) []approval.Pending {
	t.Helper()
	code, body := h.request(t, "GET", "/api/v1/approvals", nil)
	if code != 200 {
		t.Fatalf("list: %d %s", code, body)
	}
	var pending []approval.Pending
	if err := json.Unmarshal(body, &pending); err != nil {
		t.Fatal(err)
	}
	return pending
}

func (h *approvalHarness) wait(t *testing.T, n int) []approval.Pending {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		result := h.list(t)
		if len(result) == n {
			return result
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 %d 个审批超时", n)
	return nil
}

const approvalInput = `{"session_id":"approval-session","turn_id":"turn-1","cwd":"/tmp/dora","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"printf dora-approval-private-marker","description":"验证审批"}}`

func startApproval(t *testing.T, h *approvalHarness, ctx context.Context, input string) <-chan string {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		var output bytes.Buffer
		err := h.emitter.EmitInteractive(ctx, strings.NewReader(input), &output)
		if err != nil {
			done <- "ERROR: " + err.Error()
		} else {
			done <- output.String()
		}
	}()
	return done
}

func receiveApproval(t *testing.T, done <-chan string) string {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("Hook 未退出")
		return ""
	}
}

func TestInteractiveApprovalRoundTrip(t *testing.T) {
	for _, decision := range []string{"allow", "deny", "fallback"} {
		t.Run(decision, func(t *testing.T) {
			h := newApprovalHarness(t)
			h.list(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := startApproval(t, h, ctx, approvalInput)
			pending := h.wait(t, 1)[0]
			if !strings.Contains(pending.Detail, "dora-approval-private-marker") {
				t.Fatal("缺少决策所需详情")
			}
			if !strings.Contains(pending.Detail, `"cwd": "/tmp/dora"`) {
				t.Fatal("相对路径操作缺少工作目录")
			}
			for _, path := range []string{"/api/v1/runtime", "/api/v1/attention", "/api/v1/diagnostics"} {
				_, body := h.request(t, "GET", path, nil)
				if bytes.Contains(body, []byte("dora-approval-private-marker")) {
					t.Fatalf("详情泄露到 %s", path)
				}
			}
			code, _ := h.request(t, "POST", "/api/v1/approvals", map[string]any{"id": pending.ID, "decision": decision})
			if code != 200 {
				t.Fatalf("decide: %d", code)
			}
			output := receiveApproval(t, done)
			if decision == "fallback" {
				if output != "" {
					t.Fatalf("fallback stdout: %q", output)
				}
			} else {
				var value struct {
					Output struct {
						Event    string `json:"hookEventName"`
						Decision struct {
							Behavior string `json:"behavior"`
						} `json:"decision"`
					} `json:"hookSpecificOutput"`
				}
				if json.Unmarshal([]byte(output), &value) != nil || value.Output.Event != "PermissionRequest" || value.Output.Decision.Behavior != decision {
					t.Fatalf("decision stdout: %q", output)
				}
			}
			code, _ = h.request(t, "POST", "/api/v1/approvals", map[string]any{"id": pending.ID, "decision": "allow"})
			if code != 409 {
				t.Fatalf("重复点击: %d", code)
			}
			h.wait(t, 0)
			for _, path := range []string{h.dbPath, h.dbPath + "-wal"} {
				data, _ := os.ReadFile(path)
				if bytes.Contains(data, []byte("dora-approval-private-marker")) {
					t.Fatal("工具详情被持久化")
				}
			}
		})
	}
}

func TestInteractiveApprovalCancellationAndIsolation(t *testing.T) {
	h := newApprovalHarness(t)
	h.list(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := startApproval(t, h, ctx, approvalInput)
	first := h.wait(t, 1)[0]
	b := startApproval(t, h, ctx, approvalInput)
	requests := h.wait(t, 2)
	var second approval.Pending
	for _, r := range requests {
		if r.ID != first.ID {
			second = r
		}
	}
	h.request(t, "POST", "/api/v1/approvals", map[string]any{"id": second.ID, "decision": "deny"})
	if output := receiveApproval(t, b); !strings.Contains(output, `"deny"`) {
		t.Fatal(output)
	}
	select {
	case <-a:
		t.Fatal("第二个审批影响了第一个 Hook")
	default:
	}
	cancel()
	if output := receiveApproval(t, a); output != "" {
		t.Fatalf("取消不能批准: %q", output)
	}
	h.wait(t, 0)
	code, _ := h.request(t, "POST", "/api/v1/approvals", map[string]any{"id": first.ID, "decision": "allow"})
	if code != 409 {
		t.Fatalf("过期点击: %d", code)
	}
}

func TestInteractiveApprovalFallbackAndInterrupt(t *testing.T) {
	t.Run("service unavailable", func(t *testing.T) {
		h := newApprovalHarness(t)
		h.server.Close()
		var output bytes.Buffer
		if err := h.emitter.EmitInteractive(context.Background(), strings.NewReader(approvalInput), &output); err != nil || output.Len() != 0 {
			t.Fatalf("服务不可用未静默回原生审批: %v %q", err, output.String())
		}
	})
	t.Run("deadline", func(t *testing.T) {
		h := newApprovalHarness(t)
		h.list(t)
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		done := startApproval(t, h, ctx, approvalInput)
		pending := h.wait(t, 1)[0]
		if out := receiveApproval(t, done); out != "" {
			t.Fatal(out)
		}
		h.wait(t, 0)
		code, _ := h.request(t, "POST", "/api/v1/approvals", map[string]any{"id": pending.ID, "decision": "allow"})
		if code != 409 {
			t.Fatal("超时请求仍可批准")
		}
	})
	t.Run("oversized detail", func(t *testing.T) {
		h := newApprovalHarness(t)
		h.list(t)
		input := strings.Replace(approvalInput, "printf dora-approval-private-marker", strings.Repeat("x", approval.MaxDetailBytes+1), 1)
		done := startApproval(t, h, context.Background(), input)
		if out := receiveApproval(t, done); out != "" {
			t.Fatal(out)
		}
		h.wait(t, 0)
	})
	t.Run("no UI", func(t *testing.T) {
		h := newApprovalHarness(t)
		done := startApproval(t, h, context.Background(), approvalInput)
		if out := receiveApproval(t, done); out != "" {
			t.Fatal(out)
		}
	})
	t.Run("CLI stays observational", func(t *testing.T) {
		h := newApprovalHarness(t)
		h.list(t)
		h.emitter.detector = fixedDetector{surface: Surface{Name: domain.CodexSurfaceCLI}}
		done := startApproval(t, h, context.Background(), approvalInput)
		if out := receiveApproval(t, done); out != "" {
			t.Fatal(out)
		}
		h.wait(t, 0)
	})
	t.Run("interrupt", func(t *testing.T) {
		h := newApprovalHarness(t)
		h.list(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := startApproval(t, h, ctx, approvalInput)
		h.wait(t, 1)
		if err := h.emitter.Emit(ctx, strings.NewReader(`{"session_id":"approval-session","hook_event_name":"Interrupt"}`)); err != nil {
			t.Fatal(err)
		}
		if out := receiveApproval(t, done); out != "" {
			t.Fatal(out)
		}
		h.wait(t, 0)
	})
}

func TestApprovalAPIRejectsUnauthenticatedAndCrossOrigin(t *testing.T) {
	h := newApprovalHarness(t)
	for _, path := range []string{"/api/v1/approvals", "/api/v1/hooks/codex/approval"} {
		for _, origin := range []string{"", "https://untrusted.example"} {
			req, _ := http.NewRequest("POST", h.server.URL+path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", origin)
			if origin != "" {
				req.Header.Set("X-Dora-Control-Token", "approval-test")
			}
			resp, err := h.server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 403 {
				t.Fatalf("%s: %d", path, resp.StatusCode)
			}
		}
	}
}
