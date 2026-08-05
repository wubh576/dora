package domain

import "time"

const (
	RuntimeStateRunning = "running"
	RuntimeStateWaiting = "waiting"
	RuntimeStateIdle    = "idle"

	CodexSurfaceApp     = "codex_app"
	CodexSurfaceCLI     = "codex_cli"
	CodexSurfaceUnknown = "unknown"

	TerminalITerm2   = "iterm2"
	TerminalTerminal = "terminal"
	TerminalUnknown  = ""

	AttentionPermission       = "permission"
	AttentionDangerousCommand = "dangerous_command"
	AttentionUserQuestion     = "user_question"
)

type CodexHookEvent struct {
	ExternalSessionID  string
	EventName          string
	SessionStartSource string
	TurnID             string
	SubagentEvent      bool
	SubagentScope      string
	CWDBasename        string
	Model              string
	Surface            string
	TerminalKind       string
	TTY                string
	ToolName           string
	ToolUseKey         string
	ToolInputKey       string
	EventKey           string
	PromptPreview      string
	ReceivedAt         time.Time
}

type RuntimeSession struct {
	ID                int64
	Provider          string
	ExternalSessionID string
	CWDBasename       string
	SessionName       string
	Model             string
	Surface           string
	TerminalKind      string
	TTY               string
	State             string
	BaseState         string
	PromptPreview     string
	LastSeenAt        time.Time
}

type AttentionRequest struct {
	ID               int64
	RuntimeSessionID int64
	EventKey         string
	Kind             string
	Summary          string
	TurnID           string
	CreatedAt        time.Time
	NotifiedAt       *time.Time
	ResolvedAt       *time.Time
	ResolutionReason string
}

type WaitingSession struct {
	Session      RuntimeSession
	Latest       AttentionRequest
	WaitingSince time.Time
	RequestCount int
}

type ActiveSession struct {
	Session      RuntimeSession
	Latest       *AttentionRequest
	WaitingSince time.Time
	RequestCount int
}
