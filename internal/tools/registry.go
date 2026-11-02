package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// CacheKey = sha256(name, version, tree, [base], canonical(args), env).
type CacheKey [32]byte

func (k CacheKey) String() string { return fmt.Sprintf("%x", k[:]) }

// Event is reported to Options.Observer after every invocation.
type Event struct {
	Tool   string
	Key    CacheKey
	Cached bool
	Result Result
	Err    error
}

// Options configures a registry.
type Options struct {
	// Cache stores results of Pure and Hermetic tools. nil disables caching.
	Cache Cache
	// MaxConcurrent bounds concurrent tool runs (default GOMAXPROCS).
	MaxConcurrent int
	// EnvFingerprint is folded into every Hermetic cache key (e.g. OS/arch,
	// allow-listed environment values).
	EnvFingerprint string
	Observer       func(Event)
}

// Registry implements the RFC §3.3 Registry interface.
type Registry struct {
	opts   Options
	global chan struct{}

	mu     sync.RWMutex
	tools  map[string]*entry
	dirMu  sync.Mutex
	dirLks map[string]*sync.RWMutex
}

type entry struct {
	tool   Tool
	spec   Spec
	schema *jsonschema.Schema
	sem    chan struct{}
}

// New returns an empty registry.
func New(o Options) *Registry {
	n := o.MaxConcurrent
	if n <= 0 {
		n = runtime.GOMAXPROCS(0)
	}
	if o.EnvFingerprint == "" {
		o.EnvFingerprint = runtime.GOOS + "/" + runtime.GOARCH
	}
	return &Registry{opts: o, global: make(chan struct{}, n), tools: map[string]*entry{}, dirLks: map[string]*sync.RWMutex{}}
}

// Register adds a tool. Its input schema is compiled once here.
func (r *Registry) Register(t Tool) error {
	s := t.Spec()
	if s.Name == "" || s.Version == "" {
		return fmt.Errorf("tools: tool spec needs a name and a version")
	}
	schemaDoc := s.InputSchema
	if len(schemaDoc) == 0 {
		schemaDoc = json.RawMessage(`{"type":"object"}`)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaDoc))
	if err != nil {
		return fmt.Errorf("tools: %s: input schema: %w", s.Name, err)
	}
	c := jsonschema.NewCompiler()
	url := "spine://tools/" + s.Name + ".json"
	if err := c.AddResource(url, doc); err != nil {
		return fmt.Errorf("tools: %s: input schema: %w", s.Name, err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		return fmt.Errorf("tools: %s: input schema: %w", s.Name, err)
	}
	e := &entry{tool: t, spec: s, schema: sch}
	if s.MaxConcurrent > 0 {
		e.sem = make(chan struct{}, s.MaxConcurrent)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[s.Name]; dup {
		return fmt.Errorf("%w: %s", ErrDuplicate, s.Name)
	}
	r.tools[s.Name] = e
	return nil
}

// MustRegister registers tools and panics on error (for built-in sets).
func (r *Registry) MustRegister(ts ...Tool) {
	for _, t := range ts {
		if err := r.Register(t); err != nil {
			panic(err)
		}
	}
}

// Lookup returns a registered tool.
func (r *Registry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.tools[name]
	if !ok {
		return nil, false
	}
	return e.tool, true
}

// List returns all specs sorted by name.
func (r *Registry) List() []Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Spec, 0, len(r.tools))
	for _, e := range r.tools {
		out = append(out, e.spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) entry(name string) (*entry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	return e, nil
}

// Canonicalize validates args against the tool's schema and returns them in
// canonical form (sorted keys, no insignificant whitespace).
func (r *Registry) Canonicalize(name string, args json.RawMessage) (json.RawMessage, error) {
	e, err := r.entry(name)
	if err != nil {
		return nil, err
	}
	return e.canonical(args)
}

func (e *entry) canonical(args json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidArgs, e.spec.Name, err)
	}
	if err := e.schema.Validate(v); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidArgs, e.spec.Name, err)
	}
	out, err := json.Marshal(v) // map keys are emitted sorted
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidArgs, e.spec.Name, err)
	}
	return out, nil
}

// Key computes the cache key for an invocation with canonical args.
func (r *Registry) key(ctx context.Context, e *entry, inv Invocation) (CacheKey, error) {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	field(e.spec.Name)
	field(e.spec.Version)
	field(inv.Tree)
	if e.spec.UsesBase && inv.BaseKey != "" {
		field("treekey")
		field(inv.BaseKey)
	} else if e.spec.UsesBase {
		field(inv.Base)
		field(inv.Commit)
	} else {
		field("")
		field("")
	}
	field(string(inv.Args))
	if e.spec.Determinism == Hermetic {
		field(r.opts.EnvFingerprint)
		if k, ok := e.tool.(EnvKeyer); ok {
			ek, err := k.EnvKey(ctx, inv)
			if err != nil {
				return CacheKey{}, err
			}
			field(ek)
		}
	}
	var k CacheKey
	copy(k[:], h.Sum(nil))
	return k, nil
}

// Invoke validates args, consults the cache, enforces concurrency and
// timeouts, runs the tool and records the result.
func (r *Registry) Invoke(ctx context.Context, name string, inv Invocation) (Result, error) {
	e, err := r.entry(name)
	if err != nil {
		return Result{}, err
	}
	if inv.Args, err = e.canonical(inv.Args); err != nil {
		return Result{}, err
	}
	cacheable := e.spec.Determinism != Effectful && r.opts.Cache != nil
	if e.spec.Determinism != Effectful && inv.Tree == "" {
		return Result{}, fmt.Errorf("%w: %s", ErrNoTree, name)
	}
	var key CacheKey
	if cacheable {
		if key, err = r.key(ctx, e, inv); err != nil {
			return Result{}, fmt.Errorf("tools: %s: env key: %w", name, err)
		}
		if res, ok, err := r.opts.Cache.Get(ctx, key); err != nil {
			return Result{}, fmt.Errorf("tools: cache get: %w", err)
		} else if ok {
			res.Cached = true
			r.observe(Event{Tool: name, Key: key, Cached: true, Result: res})
			return res, nil
		}
	}

	if err := acquire(ctx, r.global); err != nil {
		return Result{}, err
	}
	defer func() { <-r.global }()
	if e.sem != nil {
		if err := acquire(ctx, e.sem); err != nil {
			return Result{}, err
		}
		defer func() { <-e.sem }()
	}
	if e.spec.NeedsDir {
		dir, err := inv.WorkDir(ctx)
		if err != nil {
			return Result{}, fmt.Errorf("tools: %s: %w", name, err)
		}
		inv.Dir = dir
		lk := r.dirLock(dir)
		if e.spec.ReadOnly {
			lk.RLock()
			defer lk.RUnlock()
		} else {
			lk.Lock()
			defer lk.Unlock()
		}
	}
	timeout := e.spec.Timeout
	if inv.Timeout > 0 {
		timeout = inv.Timeout
	}
	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	start := time.Now()
	res, err := e.tool.Run(runCtx, inv)
	if err != nil {
		r.observe(Event{Tool: name, Key: key, Err: err})
		return Result{}, fmt.Errorf("tools: %s: %w", name, err)
	}
	res.Tool = name
	res.Cached = false
	if res.Duration == 0 {
		res.Duration = time.Since(start)
	}
	if res.ExitCode != 0 && !hasErrors(res.Diagnostics) {
		res.Unparsed = true
	}
	clampResult(&res)
	if cacheable && !res.NoCache {
		if err := r.opts.Cache.Put(ctx, key, res); err != nil {
			return Result{}, fmt.Errorf("tools: cache put: %w", err)
		}
	}
	r.observe(Event{Tool: name, Key: key, Result: res})
	return res, nil
}

func (r *Registry) observe(ev Event) {
	if r.opts.Observer != nil {
		r.opts.Observer(ev)
	}
}

func (r *Registry) dirLock(dir string) *sync.RWMutex {
	r.dirMu.Lock()
	defer r.dirMu.Unlock()
	l, ok := r.dirLks[dir]
	if !ok {
		l = &sync.RWMutex{}
		r.dirLks[dir] = l
	}
	return l
}

func acquire(ctx context.Context, sem chan struct{}) error {
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func hasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// clampResult enforces the message and count caps, keeping errors first so
// truncation never hides a failure behind warnings.
func clampResult(res *Result) {
	if len(res.Diagnostics) > MaxDiagnostics {
		sort.SliceStable(res.Diagnostics, func(i, j int) bool {
			return rank(res.Diagnostics[i].Severity) < rank(res.Diagnostics[j].Severity)
		})
		res.Diagnostics = res.Diagnostics[:MaxDiagnostics]
		res.Truncated = true
	}
	for i := range res.Diagnostics {
		res.Diagnostics[i].Message = TruncateUTF8(res.Diagnostics[i].Message, MaxMessageBytes)
	}
	if res.Diagnostics == nil {
		res.Diagnostics = []Diagnostic{}
	}
}

func rank(s Severity) int {
	switch s {
	case Error:
		return 0
	case Warning:
		return 1
	default:
		return 2
	}
}

// TruncateUTF8 cuts s to at most n bytes on a rune boundary.
func TruncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
