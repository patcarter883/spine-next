package inbox

import (
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/internal/api"
)

func escalated(p *api.Proposal, scope ...string) api.Task {
	return api.Task{ID: "t", Spec: api.Spec{Intent: "do it", BaseRef: "refs/heads/main", Scope: scope},
		Status: api.Status{Phase: api.PhaseEscalated, Proposal: p}}
}

func TestAcceptSendsTheProposedCommand(t *testing.T) {
	c, err := Accept(escalated(&api.Proposal{Kind: api.ProposeHint, Text: "rename the helper"}), "pat")
	if err != nil || c.Kind != Hint || c.Text != "rename the helper" || c.By != "pat" || c.Task != "t" {
		t.Fatalf("hint: %+v %v", c, err)
	}
	c, err = Accept(escalated(&api.Proposal{Kind: api.ProposeRetry}), "pat")
	if err != nil || c.Kind != Retry || c.Text != "" {
		t.Fatalf("retry: %+v %v", c, err)
	}
	tk := escalated(&api.Proposal{Kind: api.ProposeScope, Text: "needs lib", Scope: []string{"pkg/**", "lib/**"}}, "pkg/**")
	c, err = Accept(tk, "pat")
	if err != nil || c.Kind != Edit || c.Text != "needs lib" || strings.Join(c.Spec.Scope, ",") != "pkg/**,lib/**" {
		t.Fatalf("scope: %+v %v", c, err)
	}
	if strings.Join(tk.Spec.Scope, ",") != "pkg/**" {
		t.Fatalf("the task's own spec changed: %v", tk.Spec.Scope)
	}
}

func TestAcceptRefusesWhatCannotApply(t *testing.T) {
	running := escalated(&api.Proposal{Kind: api.ProposeRetry})
	running.Status.Phase = api.PhaseWorking
	for name, tk := range map[string]api.Task{
		"no proposal":   escalated(nil),
		"not escalated": running,
		"empty hint":    escalated(&api.Proposal{Kind: api.ProposeHint}),
		"no new scope":  escalated(&api.Proposal{Kind: api.ProposeScope, Scope: []string{"pkg/**"}}, "pkg/**"),
		"whole repo":    escalated(&api.Proposal{Kind: api.ProposeScope, Scope: []string{"lib/**"}}),
		"unknown kind":  escalated(&api.Proposal{Kind: "rewrite"}),
	} {
		if c, err := Accept(tk, "pat"); err == nil {
			t.Errorf("%s: accepted as %+v", name, c)
		}
	}
}
