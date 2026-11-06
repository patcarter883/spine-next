package agent

import (
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/internal/repoctx"
)

func TestSurveyPromptStatesTheGatesLimits(t *testing.T) {
	for _, tc := range []struct {
		tokens int
		want   string
	}{
		{0, "at most 4800 bytes"},
		{1200, "at most 4800 bytes"},
		{500, "at most 2000 bytes"},
	} {
		out := Prompt{Task: "s", Attempt: 1, Survey: true, NotesTokens: tc.tokens}.Render()
		if !strings.Contains(out, tc.want) || !strings.Contains(out, "wc -c .spine/context.md") {
			t.Errorf("tokens %d: want %q measured with wc -c:\n%s", tc.tokens, tc.want, out)
		}
		if !strings.Contains(out, "a YAML list of at most three strings, never a single string") {
			t.Errorf("tokens %d: caveats are not described as a list:\n%s", tc.tokens, out)
		}
	}
	if repoctx.Tokens(strings.Repeat("x", 4800)) != 1200 || repoctx.Tokens(strings.Repeat("x", 4801)) != 1201 {
		t.Fatal("the stated byte cap no longer matches the gate's token estimate")
	}
}
