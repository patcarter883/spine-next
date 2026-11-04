package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/repoctx"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// Context checks onboarding: a gates file at the base (stage 1), and the
// repository context (stage 2: .spine/modules.yaml and .spine/context.md),
// including whether it has gone stale.
func Context(ctx context.Context, repoDir string) []Finding {
	var out []Finding
	add := func(l Level, format string, args ...any) {
		out = append(out, Finding{Level: l, Check: "context", Message: fmt.Sprintf(format, args...)})
	}
	primary, err := worktree.PrimaryOf(ctx, repoDir)
	if err != nil {
		return nil
	}
	cfg, err := config.Load(primary)
	if err != nil {
		return nil
	}
	base := strings.TrimPrefix(cfg.Base, "refs/heads/")
	if _, ok, _ := gate.LoadConfig(ctx, primary, cfg.Base); !ok {
		add(Warn, "onboarding stage 1: no %s at %s; write one and check it with `spine gates check`", gate.ConfigPath, base)
	}
	rc, err := repoctx.Load(ctx, primary, cfg.Base, cfg.Context.NotesTokens)
	if err != nil {
		add(Warn, "%v", err)
		return out
	}
	switch {
	case rc.ModErr != nil:
		add(Fail, "%v", rc.ModErr)
	case rc.Modules == nil && rc.Notes == "":
		add(Warn, "onboarding stage 2: no repository context at %s; run `spine survey` (agents then get .spine/context.md and planners the module map)", base)
	case rc.Modules == nil:
		add(Warn, "no .spine/modules.yaml at %s: run `spine survey` to derive it", base)
	default:
		if n := repoctx.Staleness(ctx, primary, cfg.Base, rc.Modules); n > cfg.Context.StaleAfter {
			add(Warn, "the module map is %d commits behind %s (refresh after %d): run `spine survey`", n, base, cfg.Context.StaleAfter)
		} else if n >= 0 {
			add(OK, "module map: %d modules, %d co-change pairs, %d commits behind %s", len(rc.Modules.Modules), len(rc.Modules.CoChange), n, base)
		}
	}
	if rc.Truncated {
		add(Warn, ".spine/context.md is about %d tokens; agents get the first %d (context.notes_tokens)", rc.NotesSize, cfg.Context.NotesTokens)
	} else if rc.Notes != "" {
		add(OK, ".spine/context.md: about %d tokens (cap %d)", rc.NotesSize, cfg.Context.NotesTokens)
	}
	return out
}
