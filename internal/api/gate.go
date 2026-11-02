// Package api holds Spine's domain types. It imports nothing internal;
// everything else imports it (RFC §2).
package api

import "time"

// Severity of a diagnostic.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Diagnostic is one structured finding from a deterministic tool (RFC §3.3).
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"` // compiler/linter/test id, e.g. "TestFoo", "SA4006", "TS2322"
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Col      int      `json:"col,omitempty"`
	Message  string   `json:"message"` // capped at 2 KiB
	// Fingerprint identifies "the same failure" across trees so
	// baseline-relative gates can subtract pre-existing failures. It never
	// includes line/column numbers or absolute paths.
	Fingerprint string `json:"fingerprint"`
}

// GatePolicy says how a gate's result becomes a verdict (RFC §5.4).
type GatePolicy string

const (
	// Absolute passes iff the tool exits 0.
	Absolute GatePolicy = "absolute"
	// BaselineRelative passes iff there are no error diagnostics whose
	// fingerprint is absent from the same tool's result on the base tree.
	// A failure with no diagnostics is never excused.
	BaselineRelative GatePolicy = "baseline"
)

// GateRef is one deterministic acceptance criterion.
type GateRef struct {
	Name    string         `json:"name" yaml:"-"`
	Tool    string         `json:"tool" yaml:"tool"`
	Args    map[string]any `json:"args,omitempty" yaml:"args,omitempty"`
	Policy  GatePolicy     `json:"policy,omitempty" yaml:"policy,omitempty"`
	Timeout time.Duration  `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

// GateVerdict is the gate engine's decision for one gate. Only the gate
// engine produces it (RFC §4.3).
type GateVerdict struct {
	Gate     string     `json:"gate"`
	Tool     string     `json:"tool"`
	Policy   GatePolicy `json:"policy"`
	ExitCode int        `json:"exit_code"` // 0 = pass
	Skipped  bool       `json:"skipped,omitempty"`
	// ToolExitCode is the raw exit code on the evaluated tree; BaseExitCode
	// the raw exit code on the base tree (baseline policy only).
	ToolExitCode int          `json:"tool_exit_code"`
	BaseExitCode *int         `json:"base_exit_code,omitempty"`
	New          []Diagnostic `json:"new,omitempty"`   // failures attributed to the change
	Fixed        []string     `json:"fixed,omitempty"` // baseline fingerprints no longer present
	Excused      int          `json:"excused,omitempty"`
	Metrics      map[string]float64 `json:"metrics,omitempty"`
	Cached       bool         `json:"cached"`
	Duration     time.Duration `json:"duration_ns"`
	Reason       string       `json:"reason,omitempty"` // CamelCase machine reason
}

// Passed reports whether the verdict lets the task proceed.
func (v GateVerdict) Passed() bool { return v.ExitCode == 0 && !v.Skipped }
