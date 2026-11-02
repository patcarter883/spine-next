package api

import (
	"fmt"
	"time"
)

// TaskID identifies a task. It is also the worktree lease name, so it obeys
// the lease naming rule ([A-Za-z0-9][A-Za-z0-9._-]{0,63}).
type TaskID string

// Spec is desired state (RFC §3.1). It is immutable once the task leaves
// Pending, except through a new generation (retry, operator edit).
type Spec struct {
	Generation int64            `json:"generation"`
	Title      string           `json:"title"`
	Intent     string           `json:"intent"`
	BaseRef    string           `json:"base_ref"` // full ref, e.g. refs/heads/main
	Scope      []string         `json:"scope,omitempty"`
	Acceptance []GateRef        `json:"acceptance,omitempty"` // empty: .spine/gates.yaml at base
	DependsOn  []TaskID         `json:"depends_on,omitempty"`
	Budget     Budget           `json:"budget"`
	Escalation EscalationPolicy `json:"escalation"`
	Adapter    string           `json:"adapter,omitempty"` // agent adapter name; empty = config default
}

// Budget bounds a task (RFC §3.1). Zero fields take defaults.
type Budget struct {
	MaxAttempts    int           `json:"max_attempts"`
	MaxPatchRounds int           `json:"max_patch_rounds"`
	MaxWallClock   time.Duration `json:"max_wall_clock"`
	MaxTokens      int64         `json:"max_tokens"` // 0 = unlimited
	PerRunTimeout  time.Duration `json:"per_run_timeout"`
}

// WithDefaults fills zero fields.
func (b Budget) WithDefaults() Budget {
	if b.MaxAttempts <= 0 {
		b.MaxAttempts = 2
	}
	if b.MaxPatchRounds < 0 {
		b.MaxPatchRounds = 0
	} else if b.MaxPatchRounds == 0 {
		b.MaxPatchRounds = 3
	}
	if b.MaxWallClock <= 0 {
		b.MaxWallClock = 4 * time.Hour
	}
	if b.PerRunTimeout <= 0 {
		b.PerRunTimeout = 45 * time.Minute
	}
	return b
}

// EscalationPolicy holds human checkpoints. Approval is additive: it can hold
// a Verified task, never turn a failing one into a passing one (RFC §4.1).
type EscalationPolicy struct {
	RequireApprovalBeforeMerge bool `json:"require_approval_before_merge,omitempty"`
}

// Phase is the task's lifecycle position (RFC §4.2).
type Phase string

const (
	PhasePending     Phase = "Pending"
	PhaseProvisioned Phase = "Provisioned"
	PhaseWorking     Phase = "Working"
	PhaseVerifying   Phase = "Verifying"
	PhaseVerified    Phase = "Verified"
	PhaseIntegrated  Phase = "Integrated"
	PhaseEscalated   Phase = "Escalated"
	PhaseFailed      Phase = "Failed"
	PhaseCancelled   Phase = "Cancelled"
)

// Terminal phases are not reconciled. Escalated waits for a human.
func (p Phase) Terminal() bool {
	switch p {
	case PhaseIntegrated, PhaseFailed, PhaseCancelled, PhaseEscalated:
		return true
	}
	return false
}

// Done phases never change again without a new generation.
func (p Phase) Done() bool { return p == PhaseIntegrated || p == PhaseFailed || p == PhaseCancelled }

// Condition types and their sole writers (RFC §4.3).
const (
	CondProvisioned     = "Provisioned"
	CondWorkerSucceeded = "WorkerSucceeded"
	CondGatePassed      = "GatePassed"
	CondMergeable       = "Mergeable"
	CondVerified        = "Verified"
	CondIntegrated      = "Integrated"
	CondApproved        = "Approved"
)

// Writers of status fields.
const (
	SourceWorktree   = "worktree"
	SourceAgent      = "agent"
	SourceGate       = "gate"
	SourceHuman      = "human"
	SourceController = "controller"
)

// ConditionOwner maps each condition type to the only source that may write
// it. Storage rejects any other writer.
var ConditionOwner = map[string]string{
	CondProvisioned:     SourceWorktree,
	CondWorkerSucceeded: SourceAgent,
	CondGatePassed:      SourceGate,
	CondMergeable:       SourceWorktree,
	CondVerified:        SourceGate,
	CondIntegrated:      SourceWorktree,
	CondApproved:        SourceHuman,
}

// Condition is one observed fact about a task.
type Condition struct {
	Type       string    `json:"type"`
	Status     bool      `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	Message    string    `json:"message,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
	Source     string    `json:"source"`
}

// WorktreeRef identifies the task's current lease.
type WorktreeRef struct {
	Path      string `json:"path"`
	Branch    string `json:"branch"`
	BaseOID   string `json:"base_oid"`
	AttemptID string `json:"attempt_id"`
}

// SnapshotRef identifies a snapshot commit.
type SnapshotRef struct {
	Seq     int     `json:"seq"`
	Ref     string  `json:"ref"`
	Commit  string  `json:"commit"`
	Tree    string  `json:"tree"`
	Label   string  `json:"label"`
	Attempt int     `json:"attempt"`
	Score   float64 `json:"score"`
}

// OutcomeReason classifies how an agent run ended (RFC §3.4).
type OutcomeReason string

const (
	OutcomeCompleted      OutcomeReason = "Completed"
	OutcomeTimeout        OutcomeReason = "Timeout"
	OutcomeBudgetExceeded OutcomeReason = "BudgetExceeded"
	OutcomeCrashed        OutcomeReason = "Crashed"
	OutcomeCancelled      OutcomeReason = "Cancelled"
	OutcomeStalled        OutcomeReason = "Stalled"
	OutcomeRefused        OutcomeReason = "Refused"
)

// Outcome is the result of one agent run.
type Outcome struct {
	Attempt    int           `json:"attempt"`
	Round      int           `json:"round"`
	Reason     OutcomeReason `json:"reason"`
	StopReason string        `json:"stop_reason,omitempty"` // ACP stopReason
	ExitCode   int           `json:"exit_code"`
	Signal     string        `json:"signal,omitempty"`
	TokensUsed int64         `json:"tokens_used"`
	Duration   time.Duration `json:"duration_ns"`
	Message    string        `json:"message,omitempty"` // bounded
}

// GateSummary is the stored digest of one gate evaluation.
type GateSummary struct {
	Attempt      int           `json:"attempt"`
	Round        int           `json:"round"`
	Tree         string        `json:"tree"`
	Commit       string        `json:"commit"`
	OnMerge      bool          `json:"on_merge,omitempty"` // evaluated on the merge-result tree
	Passed       bool          `json:"passed"`
	FailedGate   string        `json:"failed_gate,omitempty"`
	NewCount     int           `json:"new_count"`
	Fingerprints []string      `json:"fingerprints,omitempty"` // new-failure fingerprints, sorted
	Gates        []GateVerdict `json:"gates"`
	Score        float64       `json:"score"`
}

// Op is an operation the controller started and has not seen finish. It is
// persisted so a restarted controller can tell an orphaned run from a live
// one.
type Op struct {
	Kind      string       `json:"kind"` // "agent", "gate"
	Attempt   int          `json:"attempt"`
	Round     int          `json:"round"`
	StartedAt time.Time    `json:"started_at"`
	Snapshot  *SnapshotRef `json:"snapshot,omitempty"`
	// For gates on a merge result.
	Commit  string `json:"commit,omitempty"`
	BaseTip string `json:"base_tip,omitempty"`
	OnMerge bool   `json:"on_merge,omitempty"`
}

// Feedback is what the next patch round is told. Typed, never transcripts.
type Feedback struct {
	Source      string       `json:"source"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	Conflicts   []string     `json:"conflicts,omitempty"`
	Hint        string       `json:"hint,omitempty"` // ≤500 chars
}

// Status is observed state (RFC §3.1).
type Status struct {
	ObservedGeneration int64         `json:"observed_generation"`
	Phase              Phase         `json:"phase"`
	Reason             string        `json:"reason,omitempty"`
	Message            string        `json:"message,omitempty"`
	Conditions         []Condition   `json:"conditions,omitempty"`
	Attempt            int           `json:"attempt"`
	PatchRound         int           `json:"patch_round"`
	Worktree           *WorktreeRef  `json:"worktree,omitempty"`
	Snapshots          []SnapshotRef `json:"snapshots,omitempty"`
	BestSnapshot       *SnapshotRef  `json:"best_snapshot,omitempty"`
	LastOutcome        *Outcome      `json:"last_outcome,omitempty"`
	LastGate           *GateSummary  `json:"last_gate,omitempty"`
	GateHistory        []GateSummary `json:"gate_history,omitempty"` // Gates trimmed
	Op                 *Op           `json:"op,omitempty"`
	NextFeedback       []Feedback    `json:"next_feedback,omitempty"`
	TokensUsed         int64         `json:"tokens_used"`
	StartedAt          time.Time     `json:"started_at,omitempty"`
	IntegratedCommit   string        `json:"integrated_commit,omitempty"`
}

// Cond returns the condition of the given type.
func (s *Status) Cond(t string) (Condition, bool) {
	for _, c := range s.Conditions {
		if c.Type == t {
			return c, true
		}
	}
	return Condition{}, false
}

// IsTrue reports whether condition t is set and true.
func (s *Status) IsTrue(t string) bool {
	c, ok := s.Cond(t)
	return ok && c.Status
}

// SetCondition sets (or replaces) a condition.
func (s *Status) SetCondition(t string, status bool, reason, msg, source string) {
	c := Condition{Type: t, Status: status, Reason: reason, Message: msg, ObservedAt: time.Now().UTC(), Source: source}
	for i := range s.Conditions {
		if s.Conditions[i].Type == t {
			if s.Conditions[i].Status == status && s.Conditions[i].Reason == reason && s.Conditions[i].Message == msg && s.Conditions[i].Source == source {
				return
			}
			s.Conditions[i] = c
			return
		}
	}
	s.Conditions = append(s.Conditions, c)
}

// ClearCondition removes condition t.
func (s *Status) ClearCondition(t string) {
	out := s.Conditions[:0]
	for _, c := range s.Conditions {
		if c.Type != t {
			out = append(out, c)
		}
	}
	s.Conditions = out
}

// Task is the reconciled object.
type Task struct {
	ID              TaskID    `json:"id"`
	Spec            Spec      `json:"spec"`
	Status          Status    `json:"status"`
	ResourceVersion int64     `json:"resource_version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ValidateSpec checks a spec before it is stored.
func ValidateSpec(s Spec) error {
	if s.Intent == "" {
		return fmt.Errorf("spec: intent is empty")
	}
	if len(s.BaseRef) < 6 || s.BaseRef[:5] != "refs/" {
		return fmt.Errorf("spec: base_ref %q must be a full ref (refs/heads/...)", s.BaseRef)
	}
	if len(s.Intent) > 64<<10 {
		return fmt.Errorf("spec: intent exceeds 64 KiB")
	}
	return nil
}

// ActionKind is the closed set a supervisor chooses from (RFC §3.5).
type ActionKind string

const (
	ActRunWorker   ActionKind = "RunWorker"
	ActPatchWorker ActionKind = "PatchWorker"
	ActRollback    ActionKind = "Rollback"
	ActReplan      ActionKind = "Replan"
	ActEscalate    ActionKind = "Escalate"
	ActAbandon     ActionKind = "Abandon"
)

// Decision is a supervisor's choice (audited, never a status write).
type Decision struct {
	Action   ActionKind `json:"action"`
	Snapshot string     `json:"snapshot,omitempty"`
	Hint     string     `json:"hint,omitempty"`
	Focus    []string   `json:"focus,omitempty"`
	Reason   string     `json:"reason"`
}
