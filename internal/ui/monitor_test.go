package ui

import "testing"

func TestAgentRowsCompareAgents(t *testing.T) {
	rows := agentRows(map[string]*ctxTask{
		"r/a": {adapter: "deepseek", finished: true, firstRound: true, runs: 1, tokens: 100, cost: 0.5},
		"r/b": {adapter: "deepseek", finished: true, runs: 3, escalations: 1},
		"r/c": {adapter: "glm", runs: 2, escalations: 1},
		"r/d": {runs: 1, finished: true},
		"r/p": {kind: "plan", adapter: "glm", runs: 1},
		"r/q": {adapter: "idle"},
	})
	want := []agentStat{
		{Agent: "deepseek", Finished: 2, FirstRoundPass: 1, Escalations: 1, Runs: 4, Tokens: 100, CostUSD: 0.5},
		{Agent: "default", Finished: 1, Runs: 1},
		{Agent: "glm", Escalations: 1, Runs: 2},
	}
	if len(rows) != len(want) {
		t.Fatalf("%+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d: %+v, want %+v", i, rows[i], want[i])
		}
	}
}
