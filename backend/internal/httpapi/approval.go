package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/wubh576/dora/backend/internal/approval"
	"github.com/wubh576/dora/backend/internal/attention"
	"github.com/wubh576/dora/backend/internal/domain"
	dorasqlite "github.com/wubh576/dora/backend/internal/storage/sqlite"
)

func (s *server) approvals(w http.ResponseWriter, r *http.Request) {
	if !s.validWriteRequest(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet {
		writeNoStoreJSON(w, s.approvalsBroker.List())
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID       int64  `json:"id"`
		Decision string `json:"decision"`
	}
	if !decodeApproval(w, r, &body, 1024) {
		return
	}
	if body.Decision != "allow" && body.Decision != "deny" && body.Decision != "fallback" {
		http.Error(w, "invalid decision", 400)
		return
	}
	s.approvalMu.Lock()
	defer s.approvalMu.Unlock()
	pending := s.approvalsBroker.Get(body.ID)
	if pending == nil {
		http.Error(w, "request expired", 409)
		return
	}
	status, err := s.store.AttentionRequestStatus(r.Context(), pending.EventKey)
	if err != nil || status != dorasqlite.AttentionRequestActive {
		s.approvalsBroker.Resolve(body.ID, "fallback")
		http.Error(w, "request expired", 409)
		return
	}
	if !s.approvalsBroker.Resolve(body.ID, body.Decision) {
		http.Error(w, "request expired", 409)
		return
	}
	writeNoStoreJSON(w, map[string]bool{"sent": true})
}

func (s *server) codexApproval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.validWriteRequest(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	var body struct {
		Event  attention.Event `json:"event"`
		Detail string          `json:"detail"`
	}
	if !decodeApproval(w, r, &body, 64<<10) {
		return
	}
	event, err := body.Event.Domain(s.now())
	if err != nil || event.Surface != domain.CodexSurfaceApp || event.EventName != "PermissionRequest" || len(body.Detail) == 0 || len(body.Detail) > approval.MaxDetailBytes || !json.Valid([]byte(body.Detail)) {
		http.Error(w, "invalid approval", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), approval.WaitTimeout)
	defer cancel()
	s.approvalMu.Lock()
	status, statusErr := s.store.AttentionRequestStatus(ctx, event.EventKey)
	active, loadErr := s.store.RuntimeSessions(ctx)
	var value approval.Pending
	for _, item := range active {
		if item.Session.Provider == domain.CodexSource && item.Session.ExternalSessionID == event.ExternalSessionID {
			value = approval.Pending{SessionID: item.Session.ID, Title: item.Session.SessionName, Tool: event.ToolName, Detail: body.Detail}
			if value.Title == "" {
				value.Title = item.Session.CWDBasename
			}
		}
	}
	var pending *approval.Request
	if statusErr == nil && loadErr == nil && status == dorasqlite.AttentionRequestActive && value.SessionID != 0 {
		pending, err = s.approvalsBroker.Register(ctx, value, event.EventKey)
	}
	s.approvalMu.Unlock()
	if pending == nil || err != nil {
		writeNoStoreJSON(w, map[string]string{"decision": "fallback"})
		return
	}
	defer s.approvalsBroker.Remove(pending.ID)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	decision := "fallback"
wait:
	for {
		select {
		case decision = <-pending.Result:
			break wait
		case <-ctx.Done():
			break wait
		case <-ticker.C:
			// 窗口停止轮询或状态已解除时立即归还原生审批。
			s.approvalMu.Lock()
			status, err := s.store.AttentionRequestStatus(ctx, event.EventKey)
			if err != nil || status != dorasqlite.AttentionRequestActive || !s.approvalsBroker.Available() {
				s.approvalsBroker.Resolve(pending.ID, "fallback")
			}
			s.approvalMu.Unlock()
		}
	}
	writeNoStoreJSON(w, map[string]string{"decision": decision})
}

func decodeApproval(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "JSON required", 415)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid request", 400)
		return false
	}
	return true
}
