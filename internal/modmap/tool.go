package modmap

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/tools"
)

// NotesPath is the context summary checked alongside the map.
const NotesPath = ".spine/context.md"

// CheckTool is the gate `spine.context`: the survey task's output must be a
// valid module map whose derived fields match a fresh derivation at the
// base (an agent may write only labels and caveats), and a context summary
// within its token cap.
type CheckTool struct{}

func (CheckTool) Spec() tools.Spec {
	return tools.Spec{Name: "spine.context", Version: "1", Determinism: tools.Effectful, ReadOnly: true, UsesBase: true,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"notes_tokens":{"type":"integer","minimum":1}}}`)}
}

func diagf(code, file, format string, args ...any) tools.Diagnostic {
	msg := fmt.Sprintf(format, args...)
	return tools.Diagnostic{Severity: api.SeverityError, Code: code, File: file, Message: msg, Fingerprint: "spine.context:" + code + ":" + file + ":" + msg}
}

func (CheckTool) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	var args struct {
		NotesTokens int `json:"notes_tokens"`
	}
	json.Unmarshal(inv.Args, &args)
	if args.NotesTokens <= 0 {
		args.NotesTokens = 1200
	}
	git := gitro.Repo{Dir: inv.Repo}
	var ds []tools.Diagnostic
	rev := inv.Tree
	b, ok, err := git.Show(ctx, rev, Path)
	if err != nil {
		return tools.Result{}, err
	}
	if !ok {
		ds = append(ds, diagf("Missing", Path, "%s is missing", Path))
	} else if m, err := Parse(b); err != nil {
		ds = append(ds, diagf("Invalid", Path, "%v", err))
	} else {
		var prev *Map
		if pb, ok, _ := git.Show(ctx, inv.Base, Path); ok {
			prev, _ = Parse(pb)
		}
		fresh, err := Derive(ctx, inv.Repo, inv.Base, prev, Options{})
		if err != nil {
			return tools.Result{}, err
		}
		ds = append(ds, compare(m, fresh)...)
		for _, x := range m.Modules {
			if len(x.Label) > 120 {
				ds = append(ds, diagf("LongLabel", Path, "module %s: label is %d characters; keep it under 120", x.Name, len(x.Label)))
			}
			if len(x.Caveats) > 3 {
				ds = append(ds, diagf("TooManyCaveats", Path, "module %s: %d caveats; keep at most 3", x.Name, len(x.Caveats)))
			}
		}
	}
	nb, ok, err := git.Show(ctx, rev, NotesPath)
	if err != nil {
		return tools.Result{}, err
	}
	if !ok || strings.TrimSpace(string(nb)) == "" {
		ds = append(ds, diagf("Missing", NotesPath, "%s is missing or empty", NotesPath))
	} else if n := (len(strings.TrimSpace(string(nb))) + 3) / 4; n > args.NotesTokens {
		ds = append(ds, diagf("TooLong", NotesPath, "%s is about %d tokens; the cap is %d: keep only facts an agent needs (commands, layout, conventions, no-go areas)", NotesPath, n, args.NotesTokens))
	}
	res := tools.Result{Diagnostics: ds}
	if len(ds) > 0 {
		res.ExitCode = 1
	}
	return res, nil
}

// compare reports derived fields an agent changed.
func compare(got, want *Map) []tools.Diagnostic {
	var ds []tools.Diagnostic
	strip := func(m Module) Module { m.Label, m.Caveats = "", nil; return m }
	have := map[string]Module{}
	for _, x := range got.Modules {
		have[x.Name] = x
	}
	for _, w := range want.Modules {
		g, ok := have[w.Name]
		if !ok {
			ds = append(ds, diagf("Derived", Path, "module %s is missing (derived fields come from `spine modules`; do not remove modules)", w.Name))
			continue
		}
		if !reflect.DeepEqual(strip(g), strip(w)) {
			ds = append(ds, diagf("Derived", Path, "module %s: derived fields differ from the code (only label and caveats may be edited)", w.Name))
		}
		delete(have, w.Name)
	}
	for n := range have {
		ds = append(ds, diagf("Derived", Path, "module %s does not exist in the code", n))
	}
	if !reflect.DeepEqual(got.CoChange, want.CoChange) {
		ds = append(ds, diagf("Derived", Path, "co_change differs from the history (do not edit it)"))
	}
	return ds
}
