package modmap

import (
	"context"
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/internal/testrepo"
	"github.com/patcarter883/spine-next/internal/tools"
)

func TestCheckToolNotesCapIsBytes(t *testing.T) {
	r := testrepo.New(t)
	r.Write(map[string]string{"go.mod": "module ex.com/app\n\ngo 1.22\n"})
	base := r.Commit("base")
	for _, tc := range []struct {
		size int
		want string
	}{
		{4800, ""},
		{4801, "is 4801 bytes (about 1201 tokens); the cap is 4800 bytes (1200 tokens): cut 1 bytes"},
	} {
		r.Write(map[string]string{NotesPath: strings.Repeat("x", tc.size)})
		r.Commit("notes")
		res, err := CheckTool{}.Run(context.Background(), tools.Invocation{Repo: r.Dir, Tree: r.Tree("HEAD"), Base: base})
		if err != nil {
			t.Fatal(err)
		}
		var got string
		for _, d := range res.Diagnostics {
			if d.Code == "TooLong" {
				got = d.Message
			}
		}
		if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("size %d: TooLong = %q, want %q", tc.size, got, tc.want)
		}
	}
}
