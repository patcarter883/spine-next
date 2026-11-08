package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/tools"
)

type fake struct {
	spec  tools.Spec
	runs  atomic.Int32
	exit  int
	diags []tools.Diagnostic
}

func (f *fake) Spec() tools.Spec { return f.spec }
func (f *fake) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	f.runs.Add(1)
	return tools.Result{ExitCode: f.exit, Diagnostics: f.diags}, nil
}

func newFake(name string, d tools.Determinism) *fake {
	return &fake{spec: tools.Spec{
		Name: name, Version: "1", Determinism: d,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"string"}},"additionalProperties":false}`),
	}}
}

func TestCacheHitIgnoresArgKeyOrder(t *testing.T) {
	ctx := context.Background()
	reg := tools.New(tools.Options{Cache: tools.NewMemCache()})
	f := newFake("t.pure", tools.Pure)
	reg.MustRegister(f)
	inv := tools.Invocation{Tree: "abc", Args: json.RawMessage(`{"a":1,"b":"x"}`)}
	r1, err := reg.Invoke(ctx, "t.pure", inv)
	if err != nil || r1.Cached {
		t.Fatalf("first: %+v %v", r1, err)
	}
	inv.Args = json.RawMessage(`{ "b":"x", "a":1 }`)
	r2, err := reg.Invoke(ctx, "t.pure", inv)
	if err != nil || !r2.Cached || f.runs.Load() != 1 {
		t.Fatalf("second should hit: cached=%v runs=%d err=%v", r2.Cached, f.runs.Load(), err)
	}
	inv.Tree = "def"
	if r3, _ := reg.Invoke(ctx, "t.pure", inv); r3.Cached {
		t.Fatal("different tree must miss")
	}
}

func TestInvalidArgsRejected(t *testing.T) {
	reg := tools.New(tools.Options{})
	reg.MustRegister(newFake("t.pure", tools.Pure))
	_, err := reg.Invoke(context.Background(), "t.pure", tools.Invocation{Tree: "a", Args: json.RawMessage(`{"a":"no"}`)})
	if !errors.Is(err, tools.ErrInvalidArgs) {
		t.Fatalf("want ErrInvalidArgs, got %v", err)
	}
	_, err = reg.Invoke(context.Background(), "t.pure", tools.Invocation{Tree: "a", Args: json.RawMessage(`{"zzz":1}`)})
	if !errors.Is(err, tools.ErrInvalidArgs) {
		t.Fatalf("additionalProperties: want ErrInvalidArgs, got %v", err)
	}
	if _, err := reg.Invoke(context.Background(), "nope", tools.Invocation{}); !errors.Is(err, tools.ErrUnknownTool) {
		t.Fatalf("want ErrUnknownTool, got %v", err)
	}
	if _, err := reg.Invoke(context.Background(), "t.pure", tools.Invocation{}); !errors.Is(err, tools.ErrNoTree) {
		t.Fatalf("want ErrNoTree, got %v", err)
	}
}

func TestEffectfulNeverCachedAndUnparsedFlag(t *testing.T) {
	reg := tools.New(tools.Options{Cache: tools.NewMemCache()})
	f := newFake("t.eff", tools.Effectful)
	f.exit = 2 // failing with no diagnostics
	reg.MustRegister(f)
	for i := 0; i < 2; i++ {
		r, err := reg.Invoke(context.Background(), "t.eff", tools.Invocation{})
		if err != nil || r.Cached || !r.Unparsed {
			t.Fatalf("run %d: %+v %v", i, r, err)
		}
	}
	if f.runs.Load() != 2 {
		t.Fatalf("runs=%d", f.runs.Load())
	}
}

func TestDiagnosticCapsKeepErrors(t *testing.T) {
	reg := tools.New(tools.Options{})
	f := newFake("t.many", tools.Pure)
	for i := 0; i < tools.MaxDiagnostics+10; i++ {
		f.diags = append(f.diags, tools.Diagnostic{Severity: tools.Warning, Message: "w"})
	}
	big := make([]byte, 5000)
	for i := range big {
		big[i] = 'x'
	}
	f.diags = append(f.diags, tools.Diagnostic{Severity: tools.Error, Message: string(big)})
	f.exit = 1
	reg.MustRegister(f)
	r, err := reg.Invoke(context.Background(), "t.many", tools.Invocation{Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Diagnostics) != tools.MaxDiagnostics || !r.Truncated || r.Diagnostics[0].Severity != tools.Error ||
		len(r.Diagnostics[0].Message) != tools.MaxMessageBytes {
		t.Fatalf("caps: n=%d trunc=%v first=%v len=%d", len(r.Diagnostics), r.Truncated, r.Diagnostics[0].Severity, len(r.Diagnostics[0].Message))
	}
}

func TestStoreCachePersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "spine.db")
	st, err := storage.Open(ctx, path, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.New(tools.Options{Cache: tools.StoreCache{S: st}})
	f := newFake("t.pure", tools.Pure)
	f.diags = []tools.Diagnostic{{Severity: tools.Error, Code: "X", Message: "m", Fingerprint: "fp"}}
	f.exit = 1
	reg.MustRegister(f)
	if _, err := reg.Invoke(ctx, "t.pure", tools.Invocation{Tree: "t"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = storage.Open(ctx, path, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg2 := tools.New(tools.Options{Cache: tools.StoreCache{S: st}})
	f2 := newFake("t.pure", tools.Pure)
	reg2.MustRegister(f2)
	r, err := reg2.Invoke(ctx, "t.pure", tools.Invocation{Tree: "t"})
	if err != nil || !r.Cached || f2.runs.Load() != 0 || r.ExitCode != 1 || r.Diagnostics[0].Fingerprint != "fp" {
		t.Fatalf("persisted hit: %+v %v runs=%d", r, err, f2.runs.Load())
	}
}

func TestNormalizeMessage(t *testing.T) {
	got := tools.NormalizeMessage("/work/x/pkg/a.go:12:5: undefined: foo (0.02s) at 0xc000123", "/work/x")
	if got != "pkg/a.go: undefined: foo at 0x?" {
		t.Fatalf("got %q", got)
	}
}

func TestRecheckRerunsCachedHermeticFailures(t *testing.T) {
	ctx := context.Background()
	reg := tools.New(tools.Options{Cache: tools.NewMemCache()})
	suite, parser := newFake("t.suite", tools.Hermetic), newFake("t.parse", tools.Pure)
	suite.exit, parser.exit = 1, 1
	reg.MustRegister(suite)
	reg.MustRegister(parser)
	inv := tools.Invocation{Tree: "abc", Args: json.RawMessage(`{}`)}
	reg.Invoke(ctx, "t.suite", inv)
	if r, _ := reg.Invoke(ctx, "t.suite", inv); !r.Cached || suite.runs.Load() != 1 {
		t.Fatalf("without recheck a failure is reused: cached=%v runs=%d", r.Cached, suite.runs.Load())
	}
	inv.Recheck = true
	suite.exit = 0
	if r, _ := reg.Invoke(ctx, "t.suite", inv); r.Cached || r.ExitCode != 0 || suite.runs.Load() != 2 {
		t.Fatalf("a rechecked failure runs again: %+v runs=%d", r, suite.runs.Load())
	}
	if r, _ := reg.Invoke(ctx, "t.suite", inv); !r.Cached || suite.runs.Load() != 2 {
		t.Fatalf("a pass is reused: cached=%v runs=%d", r.Cached, suite.runs.Load())
	}
	reg.Invoke(ctx, "t.parse", inv)
	if r, _ := reg.Invoke(ctx, "t.parse", inv); !r.Cached || parser.runs.Load() != 1 {
		t.Fatalf("a pure tool's failure is deterministic and stays cached: runs=%d", parser.runs.Load())
	}
}
