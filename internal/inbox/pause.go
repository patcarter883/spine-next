package inbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// PauseInfo records who paused a repository. While the file exists the
// controller starts no new task (running tasks continue to completion).
// It lives next to the inbox so both the controller and the dashboard can
// read it without touching the database.
type PauseInfo struct {
	By     string    `json:"by,omitempty"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

func pausePath(inboxDir string) string { return filepath.Join(filepath.Dir(inboxDir), "paused") }

// Pause marks the repository paused.
func Pause(inboxDir string, p PauseInfo) error {
	if p.At.IsZero() {
		p.At = time.Now().UTC()
	}
	b, _ := json.Marshal(p)
	if err := os.MkdirAll(filepath.Dir(inboxDir), 0o755); err != nil {
		return err
	}
	tmp := pausePath(inboxDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, pausePath(inboxDir))
}

// Resume clears the pause.
func Resume(inboxDir string) error {
	err := os.Remove(pausePath(inboxDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Paused reports whether the repository is paused.
func Paused(inboxDir string) (PauseInfo, bool) {
	b, err := os.ReadFile(pausePath(inboxDir))
	if err != nil {
		return PauseInfo{}, false
	}
	var p PauseInfo
	json.Unmarshal(b, &p)
	return p, true
}
