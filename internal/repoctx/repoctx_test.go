package repoctx

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/internal/testrepo"
)

func TestControlSplit(t *testing.T) {
	n := 0
	for i := 0; i < 2000; i++ {
		id := fmt.Sprintf("task-%d", i)
		if Control(id, 0.3) {
			n++
		}
		if Control(id, 0.3) != Control(id, 0.3) || Control(id, 0) {
			t.Fatal("not stable")
		}
	}
	if n < 500 || n > 700 {
		t.Fatalf("control arm %d of 2000 at 30%%", n)
	}
}

func TestLoadCapsNotes(t *testing.T) {
	r := testrepo.New(t)
	r.Write(map[string]string{NotesPath: strings.Repeat("- a fact about the build\n", 400)})
	r.Commit("notes")
	c, err := Load(context.Background(), r.Dir, "HEAD", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Truncated || Tokens(c.Notes) > 100 || c.NotesSize < 2000 || c.Modules != nil {
		t.Fatalf("%+v", c)
	}
}
