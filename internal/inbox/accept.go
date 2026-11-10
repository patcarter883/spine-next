package inbox

import (
	"fmt"
	"slices"

	"github.com/patcarter883/spine-next/internal/api"
)

// Accept turns an escalated task's proposal into the command it names: a
// hint, a retry, or a spec edit that adds the proposed scope and retries.
func Accept(t api.Task, by string) (Command, error) {
	p := t.Status.Proposal
	if t.Status.Phase != api.PhaseEscalated || p == nil {
		return Command{}, fmt.Errorf("%s has no proposal to accept", t.ID)
	}
	cmd := Command{Task: t.ID, By: by, Text: p.Text, Via: "proposal"}
	switch p.Kind {
	case api.ProposeHint:
		if p.Text == "" {
			return Command{}, fmt.Errorf("%s: the proposed hint is empty", t.ID)
		}
		cmd.Kind = Hint
	case api.ProposeRetry:
		cmd.Kind = Retry
	case api.ProposeScope:
		spec := t.Spec
		if len(spec.Scope) == 0 {
			return Command{}, fmt.Errorf("%s already has the whole repository in scope", t.ID)
		}
		spec.Scope = slices.Clone(spec.Scope)
		added := 0
		for _, g := range p.Scope {
			if g != "" && !slices.Contains(spec.Scope, g) {
				spec.Scope = append(spec.Scope, g)
				added++
			}
		}
		if added == 0 {
			return Command{}, fmt.Errorf("%s: the proposal adds no scope", t.ID)
		}
		if err := api.ValidateSpec(spec); err != nil {
			return Command{}, err
		}
		cmd.Kind, cmd.Spec = Edit, &spec
	default:
		return Command{}, fmt.Errorf("%s: unknown proposal %q", t.ID, p.Kind)
	}
	return cmd, nil
}
