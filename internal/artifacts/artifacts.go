// Package artifacts is a content-addressed file store for full tool logs
// and payloads (RFC §5.3: output is capped in results; the full stream is
// kept by digest). Objects are written to a temp file, fsynced and renamed
// into place, so a crash never leaves a partial object under a digest name.
package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Ref identifies a stored object.
type Ref struct {
	Digest string // "sha256:<hex>"
	Size   int64
}

// Store keeps objects under Dir/sha256/<aa>/<rest>.
type Store struct{ Dir string }

// Writer streams an object into the store.
type Writer struct {
	s    Store
	f    *os.File
	h    hash.Hash
	n    int64
	done bool
}

// Create starts a new object.
func (s Store) Create() (*Writer, error) {
	tmp := filepath.Join(s.Dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(tmp, "obj-*")
	if err != nil {
		return nil, err
	}
	return &Writer{s: s, f: f, h: sha256.New()}, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	w.h.Write(p[:n])
	w.n += int64(n)
	return n, err
}

// Commit finishes the object and returns its reference.
func (w *Writer) Commit() (Ref, error) {
	if w.done {
		return Ref{}, errors.New("artifacts: writer already finished")
	}
	w.done = true
	name := w.f.Name()
	if err := w.f.Sync(); err != nil {
		w.f.Close()
		os.Remove(name)
		return Ref{}, err
	}
	if err := w.f.Close(); err != nil {
		os.Remove(name)
		return Ref{}, err
	}
	sum := hex.EncodeToString(w.h.Sum(nil))
	dst := w.s.path(sum)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		os.Remove(name)
		return Ref{}, err
	}
	if _, err := os.Stat(dst); err == nil {
		os.Remove(name) // identical content already stored
	} else if err := os.Rename(name, dst); err != nil {
		os.Remove(name)
		return Ref{}, err
	}
	if d, err := os.Open(filepath.Dir(dst)); err == nil {
		d.Sync()
		d.Close()
	}
	return Ref{Digest: "sha256:" + sum, Size: w.n}, nil
}

// Abort discards the object.
func (w *Writer) Abort() {
	if !w.done {
		w.done = true
		w.f.Close()
		os.Remove(w.f.Name())
	}
}

func (s Store) path(hexsum string) string {
	return filepath.Join(s.Dir, "sha256", hexsum[:2], hexsum[2:])
}

// Open returns a reader for a stored object.
func (s Store) Open(digest string) (io.ReadCloser, error) {
	hexsum, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hexsum) != 64 {
		return nil, fmt.Errorf("artifacts: bad digest %q", digest)
	}
	if _, err := hex.DecodeString(hexsum); err != nil {
		return nil, fmt.Errorf("artifacts: bad digest %q", digest)
	}
	return os.Open(s.path(hexsum))
}
