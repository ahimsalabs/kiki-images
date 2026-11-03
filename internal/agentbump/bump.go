package agentbump

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

// Tools are the pins Bump updates, by the versions.env prefix of their keys.
// APT is the base image digest and the apt snapshot together: both move
// with Ubuntu's archive.
var Tools = []string{"APT", "DOCKER", "GO", "NODE", "BUN", "JJ", "TASK", "GH", "RIPGREP", "UV", "AGENT_BROWSER", "PLAYWRIGHT", "RUSTUP", "RUST"}

// Options say what to bump.
type Options struct {
	// Only, if set, limits the bump to these tools (names from Tools).
	Only []string
	// Cooldown is how old a release must be to bump to it. The base image,
	// the apt snapshot, Docker's apt repository, Go and rustup publish no
	// release dates and are taken as they are.
	Cooldown time.Duration
	// GoMinor allows Go to move to a new minor release, not only a new
	// patch of the pinned one.
	GoMinor bool
	// Now is the time the cooldown counts back from (default time.Now).
	Now time.Time
	// HTTP is the client for the sources (default http.DefaultClient).
	HTTP *http.Client
	// GitHubToken, if set, authenticates GitHub API calls (rate limits).
	GitHubToken string
}

// Change is one tool's pin moving.
type Change struct {
	Tool  string `json:"tool"`
	Old   string `json:"old"`
	New   string `json:"new"`
	Level Level  `json:"-"`
	// LevelName is Level, for JSON.
	LevelName string `json:"level"`
}

// Bump finds newer pins for the tools in opts and writes them into f. It
// returns what moved, in Tools order. A tool already at the newest
// acceptable release is not a change.
func Bump(ctx context.Context, f *EnvFile, opts Options) ([]Change, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.HTTP == nil {
		opts.HTTP = http.DefaultClient
	}
	src := &fetcher{client: opts.HTTP, githubToken: opts.GitHubToken}
	cutoff := opts.Now.Add(-opts.Cooldown)
	var changes []Change
	for _, tool := range Tools {
		if len(opts.Only) > 0 && !slices.Contains(opts.Only, tool) {
			continue
		}
		c, err := bumpOne(ctx, f, src, tool, opts, cutoff)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", tool, err)
		}
		if c != nil {
			c.LevelName = c.Level.String()
			changes = append(changes, *c)
		}
	}
	return changes, nil
}

func bumpOne(ctx context.Context, f *EnvFile, src *fetcher, tool string, opts Options, cutoff time.Time) (*Change, error) {
	switch tool {
	case "APT":
		return bumpAPT(ctx, f, opts.Now)
	case "DOCKER":
		codename, err := f.MustGet("UBUNTU_CODENAME")
		if err != nil {
			return nil, err
		}
		rel, err := src.docker(ctx, codename)
		if err != nil {
			return nil, err
		}
		current, _ := f.Get("DOCKER_CE_VERSION")
		return applyDocker(f, current, rel)
	case "PLAYWRIGHT":
		current, err := f.MustGet("PLAYWRIGHT_VERSION")
		if err != nil {
			return nil, err
		}
		rel, err := src.playwright(ctx, current, cutoff)
		if err != nil {
			return nil, err
		}
		return apply(f, tool, "PLAYWRIGHT_VERSION", rel, None)
	}
	key := tool + "_VERSION"
	current, err := f.MustGet(key)
	if err != nil {
		return nil, err
	}
	var rel *release
	floor := None
	switch tool {
	case "GO":
		rel, err = src.golang(ctx, current, opts.GoMinor)
	case "NODE":
		rel, err = src.node(ctx, current, cutoff)
	case "RUSTUP":
		rel, err = src.rustup(ctx)
	case "RUST":
		// Stable Rust is backward compatible and ships every six weeks; a
		// minor release merges like a patch.
		rel, err = src.rust(ctx, cutoff)
		floor = -1
	default:
		i := slices.IndexFunc(githubTools, func(t githubTool) bool { return t.key == tool })
		if i < 0 {
			return nil, fmt.Errorf("unknown tool")
		}
		rel, err = src.github(ctx, githubTools[i], cutoff)
	}
	if err != nil {
		return nil, err
	}
	return apply(f, tool, key, rel, floor)
}

// apply writes rel's values into f if rel is newer than the pin. A negative
// floor caps the change's level at Patch.
func apply(f *EnvFile, tool, key string, rel *release, floor Level) (*Change, error) {
	current, _ := f.Get(key)
	if rel == nil || rel.version == current {
		return nil, nil
	}
	cv, _ := parseVersion(current)
	nv, ok := parseVersion(rel.version)
	if ok && nv.compare(cv) < 0 {
		return nil, nil // never move a pin backwards
	}
	keys := make([]string, 0, len(rel.values))
	for k := range rel.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := f.Set(k, rel.values[k]); err != nil {
			return nil, err
		}
	}
	level := levelOf(current, rel.version)
	if floor < 0 && level > Patch {
		level = Patch
	}
	return &Change{Tool: tool, Old: current, New: rel.version, Level: level}, nil
}

// applyDocker writes a Docker release, whose pins are apt versions: the
// level compares docker-ce's upstream versions, but any package moving is a
// change.
func applyDocker(f *EnvFile, current string, rel *release) (*Change, error) {
	changed := false
	for k, v := range rel.values {
		if old, _ := f.Get(k); old != v {
			changed = true
			if err := f.Set(k, v); err != nil {
				return nil, err
			}
		}
	}
	if !changed {
		return nil, nil
	}
	level := max(levelOf(upstream(current), upstream(rel.version)), Patch)
	return &Change{Tool: "DOCKER", Old: upstream(current), New: upstream(rel.version), Level: level}, nil
}

func bumpAPT(ctx context.Context, f *EnvFile, now time.Time) (*Change, error) {
	tag, err := f.MustGet("UBUNTU_VERSION")
	if err != nil {
		return nil, err
	}
	oldDigest, err := f.MustGet("UBUNTU_DIGEST")
	if err != nil {
		return nil, err
	}
	oldSnap, err := f.MustGet("APT_SNAPSHOT")
	if err != nil {
		return nil, err
	}
	digest, err := ubuntuDigest(ctx, tag)
	if err != nil {
		return nil, err
	}
	snap := aptSnapshot(now)
	if snap < oldSnap {
		snap = oldSnap
	}
	if digest == oldDigest && snap == oldSnap {
		return nil, nil
	}
	if err := f.Set("UBUNTU_DIGEST", digest); err != nil {
		return nil, err
	}
	if err := f.Set("APT_SNAPSHOT", snap); err != nil {
		return nil, err
	}
	return &Change{Tool: "APT", Old: oldSnap + " (ubuntu:" + tag + "@" + short(oldDigest) + ")", New: aptPin(f), Level: Patch}, nil
}

func short(digest string) string {
	_, hex, _ := strings.Cut(digest, ":")
	return hex[:min(12, len(hex))]
}

// Highest is the largest level among changes.
func Highest(changes []Change) Level {
	l := None
	for _, c := range changes {
		l = max(l, c.Level)
	}
	return l
}

// Markdown describes changes as a table, for the pull request.
func Markdown(changes []Change) string {
	var b strings.Builder
	b.WriteString("| Pin | From | To | Level |\n| --- | --- | --- | --- |\n")
	for _, c := range changes {
		fmt.Fprintf(&b, "| %s | `%s` | `%s` | %s |\n", c.Tool, c.Old, c.New, c.Level)
	}
	return b.String()
}

// Diff returns what moved from old to new, two versions.env, in Tools
// order: what a pull request carries, whichever runs bumped it.
func Diff(old, new *EnvFile) []Change {
	var changes []Change
	for _, tool := range Tools {
		var o, n string
		var level Level
		switch tool {
		case "APT":
			o, n = aptPin(old), aptPin(new)
			level = Patch
		case "DOCKER":
			o, n = dockerPin(old), dockerPin(new)
			ov, _ := old.Get("DOCKER_CE_VERSION")
			nv, _ := new.Get("DOCKER_CE_VERSION")
			level = max(levelOf(upstream(ov), upstream(nv)), Patch)
		default:
			key := tool + "_VERSION"
			o, _ = old.Get(key)
			n, _ = new.Get(key)
			level = levelOf(o, n)
			if tool == "RUST" {
				level = min(level, Patch)
			}
		}
		if o != n {
			changes = append(changes, Change{Tool: tool, Old: o, New: n, Level: level, LevelName: level.String()})
		}
	}
	return changes
}

func aptPin(f *EnvFile) string {
	snap, _ := f.Get("APT_SNAPSHOT")
	tag, _ := f.Get("UBUNTU_VERSION")
	digest, _ := f.Get("UBUNTU_DIGEST")
	return snap + " (ubuntu:" + tag + "@" + short(digest) + ")"
}

func dockerPin(f *EnvFile) string {
	var parts []string
	for _, k := range dockerKeys {
		v, _ := f.Get(k)
		parts = append(parts, upstream(v))
	}
	return strings.Join(parts, " / ")
}
