package diff

import (
	"path"
	"strings"
)

// MatchGlob reports whether a slash-separated path matches pattern. Pattern
// segments use path.Match syntax; "**" matches zero or more segments; a
// trailing "/" matches everything below a directory.
func MatchGlob(pattern, p string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	return matchSegs(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 1 && pat[1] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegs(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// InScope reports whether p matches any of the globs.
func InScope(globs []string, p string) bool {
	for _, g := range globs {
		if MatchGlob(g, p) {
			return true
		}
	}
	return false
}
