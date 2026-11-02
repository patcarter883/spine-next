package exec

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	res, err := Run(context.Background(), Cmd{Argv: []string{"sh", "-c", "echo out; echo err >&2; exit 3"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || strings.TrimSpace(string(res.Stdout)) != "out" || strings.TrimSpace(string(res.Stderr)) != "err" {
		t.Fatalf("got %+v", res)
	}
	if res.OK() {
		t.Fatal("exit 3 must not be OK")
	}
}

func TestRunDoesNotInheritEnvironment(t *testing.T) {
	t.Setenv("SPINE_SECRET_SHOULD_NOT_LEAK", "x")
	res, err := Run(context.Background(), Cmd{Argv: []string{"sh", "-c", "echo ${SPINE_SECRET_SHOULD_NOT_LEAK:-unset}"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "unset" {
		t.Fatalf("env leaked: %q", got)
	}
}

func TestRunTruncatesOutput(t *testing.T) {
	res, err := Run(context.Background(), Cmd{Argv: []string{"sh", "-c", "head -c 100000 /dev/zero"}, MaxOutput: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) != 1000 || !res.Truncated {
		t.Fatalf("len=%d truncated=%v", len(res.Stdout), res.Truncated)
	}
}

// A grandchild that holds stdout open must neither hang Run nor survive it.
func TestRunKillsWholeGroupOnTimeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	res, err := Run(context.Background(), Cmd{
		Argv:    []string{"sh", "-c", "sleep 60 & echo $! > " + pidFile + "; wait"},
		Timeout: 300 * time.Millisecond,
		Grace:   200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("want TimedOut, got %+v", res)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Run took %v", d)
	}
	assertDead(t, pidFile)
}

func TestRunKillsStragglersAfterNormalExit(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	res, err := Run(context.Background(), Cmd{
		Argv: []string{"sh", "-c", "sleep 60 & echo $! > " + pidFile + "; exit 0"},
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	assertDead(t, pidFile)
}

func TestRunOnStartErrorKillsGroup(t *testing.T) {
	var got Started
	_, err := Run(context.Background(), Cmd{
		Argv:    []string{"sleep", "60"},
		OnStart: func(s Started) error { got = s; return os.ErrPermission },
	})
	if err != os.ErrPermission {
		t.Fatalf("err=%v", err)
	}
	if got.PGID <= 1 || got.StartTime == "" {
		t.Fatalf("started=%+v", got)
	}
	if syscall.Kill(got.PGID, 0) == nil {
		t.Fatal("leader still alive")
	}
}

func TestRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	res, err := Run(ctx, Cmd{Argv: []string{"sleep", "60"}, Grace: 100 * time.Millisecond})
	if err != nil || !res.Cancelled {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func assertDead(t *testing.T, pidFile string) {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		// A zombie reparented to init is dead for our purposes.
		if st, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); strings.Contains(string(st), ") Z ") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild %d survived Run", pid)
}
