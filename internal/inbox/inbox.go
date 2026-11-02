// Package inbox is the CLI → controller command channel. The controller owns
// the database's writer lock (RFC §7.1 rule 1), so other processes never write
// it. They drop command files here instead: written to a temp name, fsynced
// and renamed, so a reader sees whole commands or none. The controller applies
// each command idempotently and then deletes the file, so a crash between
// the two only replays an already-applied command.
package inbox

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
)

// Kind of command.
type Kind string

const (
	Submit  Kind = "submit"
	Approve Kind = "approve"
	Retry   Kind = "retry"
	Cancel  Kind = "cancel"
)

// Command is one request to the controller.
type Command struct {
	ID   string     `json:"id"`
	Kind Kind       `json:"kind"`
	Task api.TaskID `json:"task"`
	Spec *api.Spec  `json:"spec,omitempty"` // Submit
	At   time.Time  `json:"at"`
	By   string     `json:"by,omitempty"`
}

// Dir returns the inbox directory for a git common dir.
func Dir(commonDir string) string { return filepath.Join(commonDir, "spine", "inbox") }

// Write enqueues a command.
func Write(dir string, c Command) (string, error) {
	if c.ID == "" {
		var b [8]byte
		rand.Read(b[:])
		c.ID = hex.EncodeToString(b[:])
	}
	if c.At.IsZero() {
		c.At = time.Now().UTC()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	tmp.Close()
	name := filepath.Join(dir, fmt.Sprintf("%020d-%s.json", c.At.UnixNano(), c.ID))
	if err := os.Rename(tmp.Name(), name); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return c.ID, nil
}

// Drain calls fn for each pending command in submission order and removes
// the file when fn returns nil. A command that fails stays for the next
// drain unless fn returns a Permanent error, in which case it is moved to
// dir/rejected.
func Drain(dir string, fn func(Command) error) error {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(dir, n)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var c Command
		if err := json.Unmarshal(b, &c); err != nil {
			reject(dir, p, err)
			continue
		}
		if err := fn(c); err != nil {
			if pe, ok := err.(Permanent); ok {
				reject(dir, p, pe.Err)
				continue
			}
			return err
		}
		os.Remove(p)
	}
	return nil
}

// Entry is a pending command file.
type Entry struct {
	Path string
	Command
}

// List returns pending commands in submission order. Unparseable files are
// moved to dir/rejected.
func List(dir string) ([]Entry, error) {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var out []Entry
	for _, n := range names {
		p := filepath.Join(dir, n)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var c Command
		if err := json.Unmarshal(b, &c); err != nil {
			reject(dir, p, err)
			continue
		}
		out = append(out, Entry{Path: p, Command: c})
	}
	return out, nil
}

// Done removes an applied command.
func Done(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Reject moves a command that cannot apply to dir/rejected with the reason.
func Reject(path string, why error) { reject(filepath.Dir(path), path, why) }

// Permanent wraps an error that retrying cannot fix.
type Permanent struct{ Err error }

func (p Permanent) Error() string { return p.Err.Error() }

func reject(dir, p string, why error) {
	rd := filepath.Join(dir, "rejected")
	os.MkdirAll(rd, 0o755)
	os.Rename(p, filepath.Join(rd, filepath.Base(p)))
	os.WriteFile(filepath.Join(rd, filepath.Base(p)+".err"), []byte(why.Error()+"\n"), 0o644)
}
