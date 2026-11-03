package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNotifierDetectsAndDelivers(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]any
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	}))
	defer hook.Close()
	cfg := Config{Webhooks: []Webhook{{URL: hook.URL}, {URL: hook.URL, Format: "generic"}}, Budgets: Budgets{DailyTokens: 1000}}
	n := &Notifier{Config: func() Config { return cfg }}
	ctx := context.Background()
	// Already escalated at startup: not announced.
	n.Observe(ctx, State{Tasks: []TaskState{{Repo: "r", ID: "old", Phase: "Escalated", Reason: "NoProgress"}, {Repo: "r", ID: "a", Phase: "Working"}},
		Repos: []RepoState{{Name: "r", State: "running"}}})
	if len(n.Feed(0)) != 0 {
		t.Fatalf("seed produced %+v", n.Feed(0))
	}
	n.Observe(ctx, State{Tasks: []TaskState{
		{Repo: "r", ID: "old", Phase: "Escalated", Reason: "NoProgress"},
		{Repo: "r", ID: "a", Title: "Add A", Phase: "Escalated", Reason: "AwaitingApproval"},
		{Repo: "r", ID: "b", Phase: "Working", Tokens: 900, MaxTokens: 1000},
	}, Repos: []RepoState{{Name: "r", State: "broken", Error: "panic: boom\nstack"}}, DayTok: 1500})
	f := n.Feed(0)
	kinds := []string{}
	for _, x := range f {
		kinds = append(kinds, x.Kind)
	}
	if strings.Join(kinds, ",") != "approval,budget,broken,budget" {
		t.Fatalf("kinds %v", kinds)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 8 || !strings.Contains(got[0]["text"].(string), "Approval needed: Add A") || got[1]["kind"] != "approval" {
		t.Fatalf("webhooks %v", got)
	}
	// No repeats.
	n.Observe(ctx, State{Tasks: []TaskState{{Repo: "r", ID: "a", Phase: "Escalated", Reason: "AwaitingApproval"}}, DayTok: 2000})
	if len(n.Feed(f[len(f)-1].Seq)) != 0 {
		t.Fatal("repeated notifications")
	}
	if (Config{Webhooks: []Webhook{{URL: "ftp://x"}}}).Validate() == nil || (Config{Events: []string{"nope"}}).Validate() == nil {
		t.Fatal("bad config accepted")
	}
}
