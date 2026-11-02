// Package exec is Spine's only wrapper around os/exec (RFC 0001 §2, §5.3).
//
// Every child runs as the leader of its own process group. Run never returns
// while any process in that group is still alive: on exit, timeout or
// cancellation the whole group is signalled (SIGTERM, then SIGKILL after a
// grace period), and stragglers left in the group after a normal leader exit
// are killed too. Output is captured through capped buffers so a chatty child
// cannot exhaust memory. A non-zero exit is a Result, not an error; an error
// means the process could not be run at all (an infrastructure fault).
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/patcarter883/spine-next/third_party/gascity/pidutil"
	"github.com/patcarter883/spine-next/third_party/gascity/processgroup"
)

const (
	// DefaultGrace is the SIGTERM→SIGKILL grace period.
	DefaultGrace = 2 * time.Second
	// DefaultMaxOutput caps each of stdout and stderr.
	DefaultMaxOutput = 4 << 20
)

// Cmd describes one child process.
type Cmd struct {
	Argv []string
	Dir  string
	// Env is the complete child environment. Nil means MinimalEnv(): the
	// parent environment is never inherited wholesale.
	Env   []string
	Stdin []byte
	// StdinFile, when set, is the child's stdin (typically the read end of an
	// os.Pipe whose write end the caller keeps, for interactive protocols
	// such as ACP). Run closes it in the parent once the child has started.
	// A file, not an io.Reader, so Wait never blocks on a copy goroutine.
	StdinFile *os.File
	// Timeout bounds the run; zero means only ctx bounds it.
	Timeout time.Duration
	// Grace is the SIGTERM→SIGKILL delay; zero means DefaultGrace.
	Grace time.Duration
	// MaxOutput caps each captured stream; zero means DefaultMaxOutput.
	MaxOutput int
	// Stdout/Stderr, when set, receive the stream instead of the capture
	// buffer (the caller owns any cap). Used for streaming adapters.
	Stdout io.Writer
	Stderr io.Writer
	// OnStart is called after the child starts and before Run waits. It is
	// the lease hook: callers record (pgid, start time) durably here so that
	// crash recovery can find the group. If it returns an error the group is
	// killed and Run returns that error.
	OnStart func(Started) error
	// DisableParentDeathSignal turns off the Linux PR_SET_PDEATHSIG safety
	// net. Only the chaos harness sets it, to prove that Recover — not the
	// kernel — reaps orphans.
	DisableParentDeathSignal bool
}

// Started identifies a running process group.
type Started struct {
	PGID      int
	StartTime string // /proc start time, for PID-reuse-safe liveness checks
}

// Result is the outcome of a child that ran.
type Result struct {
	ExitCode  int
	Signal    string // non-empty if the leader died by signal
	TimedOut  bool
	Cancelled bool
	Stdout    []byte
	Stderr    []byte
	Truncated bool
	Duration  time.Duration
}

// OK reports a clean zero exit.
func (r Result) OK() bool { return r.ExitCode == 0 && r.Signal == "" && !r.TimedOut && !r.Cancelled }

// ErrEmptyArgv is returned for a Cmd without a program.
var ErrEmptyArgv = errors.New("exec: empty argv")

// MinimalEnv returns the allow-listed subset of the parent environment.
func MinimalEnv() []string {
	keep := []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "USER", "TERM", "TZ"}
	var env []string
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Run executes c and waits for its whole process group to be gone.
func Run(ctx context.Context, c Cmd) (Result, error) {
	if len(c.Argv) == 0 || c.Argv[0] == "" {
		return Result{}, ErrEmptyArgv
	}
	grace := c.Grace
	if grace <= 0 {
		grace = DefaultGrace
	}
	maxOut := c.MaxOutput
	if maxOut <= 0 {
		maxOut = DefaultMaxOutput
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	cmd := osexec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	if cmd.Env == nil {
		cmd.Env = MinimalEnv()
	}
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	if c.StdinFile != nil {
		cmd.Stdin = c.StdinFile
	}
	processgroup.StartCommandInNewGroup(cmd)
	if !c.DisableParentDeathSignal {
		setParentDeathSignal(cmd)
	}

	// Pipes are *os.File so Wait returns when the leader exits, without
	// waiting for descendants that inherited the write ends; the readers
	// finish once the group is killed and every writer is gone.
	outR, outW, err := os.Pipe()
	if err != nil {
		return Result{}, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return Result{}, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW

	stdout := &capped{max: maxOut}
	stderr := &capped{max: maxOut}
	var outDst, errDst io.Writer = stdout, stderr
	if c.Stdout != nil {
		outDst = c.Stdout
	}
	if c.Stderr != nil {
		errDst = c.Stderr
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		if c.StdinFile != nil {
			c.StdinFile.Close()
		}
		return Result{}, fmt.Errorf("exec %s: %w", c.Argv[0], err)
	}
	outW.Close()
	errW.Close()
	if c.StdinFile != nil {
		c.StdinFile.Close() // the child holds its own descriptor
	}
	pgid := cmd.Process.Pid

	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); _, _ = io.Copy(outDst, outR); outR.Close() }()
	go func() { defer readers.Done(); _, _ = io.Copy(errDst, errR); errR.Close() }()

	if c.OnStart != nil {
		st, _ := pidutil.StartTime(pgid)
		if hookErr := c.OnStart(Started{PGID: pgid, StartTime: st}); hookErr != nil {
			killGroup(pgid)
			_ = cmd.Wait()
			readers.Wait()
			return Result{}, hookErr
		}
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	res := Result{}
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			res.TimedOut = true
		} else {
			res.Cancelled = true
		}
		_ = processgroup.Terminate(pgid, grace, processgroup.Options{})
		waitErr = <-done
	}
	// The leader is gone; nothing else in its group may outlive Run.
	killGroup(pgid)
	readers.Wait()

	res.Duration = time.Since(start)
	res.Stdout, res.Stderr = stdout.Bytes(), stderr.Bytes()
	res.Truncated = stdout.truncated || stderr.truncated
	res.ExitCode, res.Signal = exitStatus(cmd, waitErr)
	return res, nil
}

func killGroup(pgid int) {
	if pgid > 1 && pgid != syscall.Getpgrp() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}

func exitStatus(cmd *osexec.Cmd, waitErr error) (int, string) {
	if cmd.ProcessState == nil {
		return -1, ""
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -1, ws.Signal().String()
	}
	if waitErr != nil {
		var ee *osexec.ExitError
		if !errors.As(waitErr, &ee) {
			return -1, ""
		}
	}
	return cmd.ProcessState.ExitCode(), ""
}

// capped is a bytes.Buffer that silently drops bytes past max.
type capped struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.max - c.buf.Len()
	if room <= 0 {
		c.truncated = c.truncated || len(p) > 0
		return len(p), nil
	}
	if len(p) > room {
		c.buf.Write(p[:room])
		c.truncated = true
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

func (c *capped) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf.Bytes()...)
}
