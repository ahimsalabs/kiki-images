package agentbump

import (
	"strconv"
	"strings"
)

// Level says how far a pin moved, for the bump's auto-merge rule: a patch
// bump merges itself once the build passes, anything larger waits for a
// review.
type Level int

const (
	None Level = iota
	Patch
	Minor
	Major
)

func (l Level) String() string {
	return [...]string{"none", "patch", "minor", "major"}[l]
}

// version is a release number: its numeric dot-separated parts, after any
// prefix such as "v" or "bun-v". It is not a full semver: the tools pinned
// here release plain numbered versions, and a tag with a pre-release or
// build suffix is not a release this bumps to.
type version []int

func parseVersion(s string) (version, bool) {
	i := strings.IndexAny(s, "0123456789")
	if i < 0 {
		return nil, false
	}
	var v version
	for _, part := range strings.Split(s[i:], ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nil, false
		}
		v = append(v, n)
	}
	return v, true
}

func (v version) part(i int) int {
	if i < len(v) {
		return v[i]
	}
	return 0
}

func (v version) compare(w version) int {
	for i := range max(len(v), len(w)) {
		if a, b := v.part(i), w.part(i); a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}
	return 0
}

// levelOf says how far from old to new moved: the first differing part
// names it. For a 0.x version, whose minor releases may break, a minor
// step counts as minor as well.
func levelOf(old, new string) Level {
	o, ok1 := parseVersion(old)
	n, ok2 := parseVersion(new)
	switch {
	case old == new:
		return None
	case !ok1 || !ok2:
		return Major
	case o.part(0) != n.part(0):
		return Major
	case o.part(1) != n.part(1):
		return Minor
	default:
		return Patch
	}
}

// sameLine reports whether v is on the release line of current for the
// given depth: depth 1 keeps the major (Node's LTS line), depth 2 the
// minor (Go's).
func sameLine(current, v version, depth int) bool {
	for i := range depth {
		if current.part(i) != v.part(i) {
			return false
		}
	}
	return true
}
