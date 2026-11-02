package acp

import "encoding/json"

// ProtocolVersion is the ACP major version this client speaks.
const ProtocolVersion = 1

// Method names (schema v1).
const (
	MethodInitialize        = "initialize"
	MethodSessionNew        = "session/new"
	MethodSessionPrompt     = "session/prompt"
	MethodSessionCancel     = "session/cancel"
	MethodSessionSetMode    = "session/set_mode"
	MethodSessionUpdate     = "session/update"
	MethodRequestPermission = "session/request_permission"
)

type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type FileSystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

// ClientCapabilities: Spine advertises neither fs nor terminal, so the agent
// edits files and runs commands itself, inside its cwd (the lease worktree).
type ClientCapabilities struct {
	FS       FileSystemCapabilities `json:"fs"`
	Terminal bool                   `json:"terminal"`
}

type InitializeRequest struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *Implementation    `json:"clientInfo,omitempty"`
}

type InitializeResponse struct {
	ProtocolVersion   int             `json:"protocolVersion"`
	AgentCapabilities json.RawMessage `json:"agentCapabilities,omitempty"`
	AuthMethods       json.RawMessage `json:"authMethods,omitempty"`
	AgentInfo         *Implementation `json:"agentInfo,omitempty"`
}

type NewSessionRequest struct {
	Cwd        string `json:"cwd"`
	McpServers []any  `json:"mcpServers"`
}

type SessionMode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

type NewSessionResponse struct {
	SessionID string            `json:"sessionId"`
	Modes     *SessionModeState `json:"modes,omitempty"`
}

type SetModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// ContentBlock: Spine only sends text.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// Usage is the token tally some agents attach to PromptResponse (Claude's
// adapter does; it is outside the stable v1 schema, so it is optional).
type Usage struct {
	InputTokens       int64 `json:"inputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	CachedReadTokens  int64 `json:"cachedReadTokens"`
	CachedWriteTokens int64 `json:"cachedWriteTokens"`
	TotalTokens       int64 `json:"totalTokens"`
}

// Stop reasons (schema v1 StopReason).
const (
	StopEndTurn         = "end_turn"
	StopMaxTokens       = "max_tokens"
	StopMaxTurnRequests = "max_turn_requests"
	StopRefusal         = "refusal"
	StopCancelled       = "cancelled"
)

type PromptResponse struct {
	StopReason string `json:"stopReason"`
	Usage      *Usage `json:"usage,omitempty"`
}

type CancelNotification struct {
	SessionID string `json:"sessionId"`
}

// SessionNotification is a session/update.
type SessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// SessionUpdate holds the fields Spine reads from any update variant.
type SessionUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content,omitempty"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	Title         string          `json:"title,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	Status        string          `json:"status,omitempty"`
	Locations     []Location      `json:"locations,omitempty"`
	Used          int64           `json:"used,omitempty"` // usage_update: context tokens in use
	Size          int64           `json:"size,omitempty"`
}

type Location struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// ToolCallUpdate as carried in a permission request.
type ToolCallUpdate struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Locations  []Location      `json:"locations,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
}

// Permission option kinds (schema v1).
const (
	AllowOnce    = "allow_once"
	AllowAlways  = "allow_always"
	RejectOnce   = "reject_once"
	RejectAlways = "reject_always"
)

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type RequestPermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

type PermissionOutcome struct {
	Outcome  string `json:"outcome"` // "selected" | "cancelled"
	OptionID string `json:"optionId,omitempty"`
}

type RequestPermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// TextOf extracts text from a ContentBlock JSON value.
func TextOf(raw json.RawMessage) string {
	var b ContentBlock
	if json.Unmarshal(raw, &b) == nil && b.Type == "text" {
		return b.Text
	}
	return ""
}
