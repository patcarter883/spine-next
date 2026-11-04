package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const probeModels = `{"providers":{"local":{"models":[{"id":"qwen"}]}}}`

// probeEnv is a contained or uncontained adapter over the fake Pi, a host
// agent dir holding files that must and must not enter the container, and a
// private TMPDIR that shows which temp files a probe leaves behind.
type probeEnv struct {
	tmp, host, piLog, argvLog, dir string
	adapter                        Adapter
}

func newProbeEnv(t *testing.T, model string) probeEnv {
	t.Helper()
	root := t.TempDir()
	e := probeEnv{tmp: filepath.Join(root, "tmp"), host: filepath.Join(root, "host-agent"),
		piLog: filepath.Join(root, "pilog"), argvLog: filepath.Join(root, "runtime.log"), dir: filepath.Join(root, "probe-dir")}
	for _, d := range []string{e.tmp, e.host, e.dir, filepath.Join(e.host, "sessions")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"models.json": probeModels, "settings.json": "{}", "auth.json": `{"k":"secret"}`} {
		if err := os.WriteFile(filepath.Join(e.host, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", e.tmp)
	t.Setenv(PiAgentDirEnv, e.host)
	e.adapter = Adapter{Name: "pi", Protocol: "pi-rpc", Argv: []string{fakePiBin}, Version: "1.5.0", Model: model,
		Env: map[string]string{"FAKE_PI_LOG": e.piLog, "FAKE_PI_CHECK_MODELS": "1"}}
	return e
}

// contain puts the adapter in a fake runtime. hang makes the RPC run never
// answer. The image is unique so versionCache cannot skip the version check.
func (e *probeEnv) contain(t *testing.T, hang bool) {
	t.Helper()
	onRun := `exec "$@"`
	if hang {
		onRun = `[ -z "$envfile" ] || { echo hanging >> "$LOG"; exec sleep 60; }
exec "$@"`
	}
	script := filepath.Join(t.TempDir(), "fake-runtime")
	body := `#!/bin/sh
LOG='` + e.argvLog + `'
echo "$*" >> "$LOG"
case "$1" in
rm) exit 0;;
run) ;;
*) exit 2;;
esac
shift
envfile= workdir= private=
while [ $# -gt 0 ]; do
	case "$1" in
	--rm|-i|--init) shift;;
	--name|--label|--network|--user|--env) shift 2;;
	--env-file) envfile=$2; shift 2;;
	--workdir) workdir=$2; shift 2;;
	--volume)
		case "$2" in *:/spine/pi-agent) private=${2%:/spine/pi-agent};; esac
		shift 2;;
	*) break;;
	esac
done
shift # the image
if [ -n "$envfile" ]; then
	while IFS= read -r line; do export "$line"; done < "$envfile"
fi
if [ -n "$private" ]; then
	ls -A "$private" | sed 's/^/listing /' >> "$LOG"
	[ "$PI_CODING_AGENT_DIR" != /spine/pi-agent ] || PI_CODING_AGENT_DIR=$private
fi
[ -z "$workdir" ] || cd "$workdir" || exit 3
` + onRun + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	image := fmt.Sprintf("spine-pi:probe-%d", time.Now().UnixNano())
	e.adapter.Container = &Container{Runtime: script, Image: image}
}

func (e probeEnv) probe(ctx context.Context) (string, error) {
	return (&Runner{}).ProbePi(ctx, e.adapter, e.dir)
}

// runLine returns the logged arguments of the probe's RPC container.
func (e probeEnv) runLine(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(e.argvLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "run --rm -i --init ") {
			return strings.Fields(line)
		}
	}
	t.Fatalf("no RPC run logged:\n%s", b)
	return nil
}

// flagValues returns every value of a flag, up to the image.
func flagValues(run []string, flag, image string) []string {
	var vals []string
	for i := 0; i+1 < len(run) && run[i] != image; i++ {
		if run[i] == flag {
			vals = append(vals, run[i+1])
		}
	}
	return vals
}

func (e probeEnv) privateDir(t *testing.T) string {
	t.Helper()
	for _, v := range flagValues(e.runLine(t), "--volume", e.adapter.Container.Image) {
		if p, ok := strings.CutSuffix(v, ":/spine/pi-agent"); ok {
			return p
		}
	}
	t.Fatalf("no private agent dir mounted: %v", e.runLine(t))
	return ""
}

// assertNoPrompt: a probe must not spend tokens, whatever happened.
func (e probeEnv) assertNoPrompt(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(e.piLog, "prompts")); !os.IsNotExist(err) {
		t.Errorf("prompts log exists (stat: %v)", err)
	}
	b, _ := os.ReadFile(filepath.Join(e.piLog, "commands"))
	if slices.Contains(strings.Split(string(b), "\n"), "prompt") {
		t.Errorf("a prompt command was received: %s", b)
	}
}

// assertCleanedUp: the container was removed, and the private dir and env
// file named on the command line are gone, as is anything else in TMPDIR.
func (e probeEnv) assertCleanedUp(t *testing.T) {
	t.Helper()
	run := e.runLine(t)
	image := e.adapter.Container.Image
	names := flagValues(run, "--name", image)
	envFiles := flagValues(run, "--env-file", image)
	if len(names) != 1 || len(envFiles) != 1 {
		t.Fatalf("name %v, env file %v in %v", names, envFiles, run)
	}
	if !strings.HasPrefix(names[0], "spine-probe-") {
		t.Errorf("container name %q", names[0])
	}
	b, _ := os.ReadFile(e.argvLog)
	if !slices.Contains(strings.Split(string(b), "\n"), "rm -f "+names[0]) {
		t.Errorf("container not removed:\n%s", b)
	}
	for _, p := range []string{envFiles[0], e.privateDir(t)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind (stat: %v)", p, err)
		}
	}
	left, err := os.ReadDir(e.tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range left {
		t.Errorf("left in TMPDIR: %s", f.Name())
	}
}

func TestProbePiContainedListedModel(t *testing.T) {
	e := newProbeEnv(t, "local/qwen")
	e.contain(t, false)
	msg, err := e.probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	image := e.adapter.Container.Image
	if want := "pi 1.5.0, model local/qwen in " + image; msg != want {
		t.Fatalf("message %q, want %q", msg, want)
	}
	if b, _ := os.ReadFile(filepath.Join(e.piLog, "commands")); !strings.Contains(string(b), "get_state") {
		t.Fatalf("commands: %q", b)
	}
	logged, _ := os.ReadFile(e.argvLog)
	if !strings.Contains(string(logged), "run --rm --network none "+image+" "+fakePiBin+" --version") {
		t.Fatalf("no version check in:\n%s", logged)
	}

	run := e.runLine(t)
	private := e.privateDir(t)
	if private == e.host {
		t.Fatalf("the host agent dir %s was mounted", e.host)
	}
	// Only the probe dir and the private agent dir: no git dirs, no repository.
	want := []string{e.dir + ":" + e.dir, private + ":/spine/pi-agent"}
	if got := flagValues(run, "--volume", image); !slices.Equal(got, want) {
		t.Fatalf("volumes %v, want %v", got, want)
	}
	if got := flagValues(run, "--workdir", image); !slices.Equal(got, []string{e.dir}) {
		t.Fatalf("workdir %v", got)
	}

	var listing []string
	for _, line := range strings.Split(string(logged), "\n") {
		if f, ok := strings.CutPrefix(line, "listing "); ok {
			listing = append(listing, f)
		}
	}
	if !slices.Equal(listing, []string{"models.json", "settings.json"}) {
		t.Fatalf("private agent dir holds %v", listing)
	}
	e.assertNoPrompt(t)
	e.assertCleanedUp(t)
}

func TestProbePiContainedMissingModel(t *testing.T) {
	e := newProbeEnv(t, "local/absent")
	e.contain(t, false)
	_, err := e.probe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Model local/absent not found") {
		t.Fatalf("err = %v", err)
	}
	e.assertNoPrompt(t)
	e.assertCleanedUp(t)
}

func TestProbePiContainedNoModel(t *testing.T) {
	e := newProbeEnv(t, "")
	e.contain(t, false)
	_, err := e.probe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no model selected") {
		t.Fatalf("err = %v", err)
	}
	e.assertNoPrompt(t)
	e.assertCleanedUp(t)
}

// A Pi that never answers: the probe gives up when the context ends, and
// still removes the container and its temp files.
func TestProbePiContainedTimeoutCleansUp(t *testing.T) {
	e := newProbeEnv(t, "local/qwen")
	e.contain(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if b, _ := os.ReadFile(e.argvLog); strings.Contains(string(b), "hanging\n") {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	_, err := e.probe(ctx)
	if err == nil || !strings.Contains(err.Error(), "pi rpc") {
		t.Fatalf("err = %v", err)
	}
	e.assertNoPrompt(t)
	e.assertCleanedUp(t)
}

// The shared path, uncontained: same model resolution, no container message.
func TestProbePiUncontained(t *testing.T) {
	e := newProbeEnv(t, "local/qwen")
	msg, err := e.probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if msg != "pi 1.5.0, model local/qwen" {
		t.Fatalf("message %q", msg)
	}
	e.assertNoPrompt(t)

	e = newProbeEnv(t, "local/absent")
	if _, err = e.probe(context.Background()); err == nil || !strings.Contains(err.Error(), "Model local/absent not found") {
		t.Fatalf("err = %v", err)
	}
	e.assertNoPrompt(t)

	e = newProbeEnv(t, "")
	if _, err = e.probe(context.Background()); err == nil || !strings.Contains(err.Error(), "no model selected") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(e.argvLog); !os.IsNotExist(err) {
		t.Fatalf("a container runtime ran for an uncontained adapter (stat: %v)", err)
	}
}

// Real runs keep their mounts and argument order; a probe differs only in
// its name, workdir and worktree volumes.
func TestContainerRunArguments(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	worktree := filepath.Join(root, "wt")
	gitdir := filepath.Join(root, "repo", ".git", "worktrees", "wt")
	commondir := filepath.Join(root, "repo", ".git")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Container{Image: "img", Network: "none", User: "1:2", Mounts: []string{"/m:/m"}, Args: []string{"--cpus", "4"}}
	expect := func(w *wrapped, workdir string, volumes ...string) {
		t.Helper()
		want := []string{"docker", "run", "--rm", "-i", "--init", "--name", w.name,
			"--label", ContainerLabel + "=lbl", "--network", "none", "--user", "1:2", "--workdir", workdir,
			"--env-file", w.envFile, "--env", "HOME=/tmp/spine-home"}
		for _, v := range volumes {
			want = append(want, "--volume", v)
		}
		want = append(want, "--cpus", "4", "img", "pi", "--mode", "rpc")
		if !slices.Equal(w.argv, want) {
			t.Fatalf("argv\n got %v\nwant %v", w.argv, want)
		}
		b, err := os.ReadFile(w.envFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "A=1\nB=2\n" { // the multi-line value is dropped
			t.Fatalf("env file %q", b)
		}
	}
	argv, env, extraEnv := []string{"pi", "--mode", "rpc"}, []string{"A=1", "BAD=x\ny"}, []string{"B=2"}

	w, err := c.wrap("lbl", worktree, "my task", 1, 2, argv, env, []string{"/x:/x"}, extraEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w.name, "spine-my-task-a1-r2-") {
		t.Fatalf("name %q", w.name)
	}
	expect(w, worktree, commondir+":"+commondir+":ro", gitdir+":"+gitdir, worktree+":"+worktree, "/x:/x", "/m:/m")

	dir := t.TempDir() // not a git worktree
	p, err := c.wrapProbe("lbl", dir, argv, env, []string{"/x:/x"}, extraEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.name, "spine-probe-") {
		t.Fatalf("name %q", p.name)
	}
	expect(p, dir, dir+":"+dir, "/x:/x", "/m:/m")

	if _, err := (&Container{}).wrapProbe("lbl", dir, argv, env, nil, nil); err == nil {
		t.Fatal("wrapProbe without an image succeeded")
	}
}
