package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
)

// Fingerprint hashes the identifying parts of a diagnostic. Callers pass
// stable parts only (tool scope, code, relative file, normalized message);
// never line numbers.
func Fingerprint(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:12])
}

var (
	reFileLoc  = regexp.MustCompile(`([\w./\\-]+\.[A-Za-z0-9]+)[:(]\d+(?:[:,]\d+)?\)?`)
	reHexAddr  = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	reDuration = regexp.MustCompile(`\(?\b\d+(?:\.\d+)?\s?(?:ns|µs|us|ms|s|m|h)\b\)?`)
	reTmpPath  = regexp.MustCompile(`(?:/tmp|/var/folders|/private/var)/[^\s:'"]+`)
	reGorout   = regexp.MustCompile(`goroutine \d+`)
	reSpace    = regexp.MustCompile(`\s+`)
	reLineWord = regexp.MustCompile(`(?i)\bline \d+`)
)

// NormalizeMessage strips content that varies between otherwise identical
// failures: the evaluation directory (roots), file:line:col positions, hex
// addresses, durations, temp paths and goroutine ids.
func NormalizeMessage(msg string, roots ...string) string {
	for _, r := range expandRoots(roots) {
		msg = strings.ReplaceAll(msg, r+"/", "")
		msg = strings.ReplaceAll(msg, r, ".")
	}
	msg = reFileLoc.ReplaceAllString(msg, "$1")
	msg = reLineWord.ReplaceAllString(msg, "line N")
	msg = reTmpPath.ReplaceAllString(msg, "<tmp>")
	msg = reHexAddr.ReplaceAllString(msg, "0x?")
	msg = reDuration.ReplaceAllString(msg, "")
	msg = reGorout.ReplaceAllString(msg, "goroutine N")
	return strings.TrimSpace(reSpace.ReplaceAllString(msg, " "))
}

// RelPath makes p relative to root when it lies under it (also under root's
// symlink-resolved form) and returns it slash-separated. Other paths are
// returned cleaned.
func RelPath(root, p string) string {
	if p == "" {
		return ""
	}
	p = strings.TrimPrefix(p, "./")
	if filepath.IsAbs(p) {
		for _, r := range expandRoots([]string{root}) {
			if rel, err := filepath.Rel(r, p); err == nil && !strings.HasPrefix(rel, "..") {
				return filepath.ToSlash(rel)
			}
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

func expandRoots(roots []string) []string {
	var out []string
	for _, r := range roots {
		if r == "" {
			continue
		}
		r = strings.TrimSuffix(r, "/")
		out = append(out, r)
		if ev, err := filepath.EvalSymlinks(r); err == nil && ev != r {
			out = append(out, ev)
		}
	}
	// Longest first, so a root nested in another is replaced before it.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(out[j]) > len(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
