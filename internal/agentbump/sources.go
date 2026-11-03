package agentbump

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// A release is what a source reports as the newest acceptable version of a
// tool, with the versions.env values that pin it.
type release struct {
	version string
	// values are the versions.env keys and values that pin it, the version
	// key included.
	values map[string]string
}

// fetcher does the HTTP for the sources.
type fetcher struct {
	client      *http.Client
	githubToken string
}

func (f *fetcher) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if f.githubToken != "" && strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+f.githubToken)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

func (f *fetcher) json(ctx context.Context, url string, v any) error {
	resp, err := f.get(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	return nil
}

func (f *fetcher) text(ctx context.Context, url string) (string, error) {
	resp, err := f.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return string(b), err
}

// sha256 downloads url and returns its sha256, for the one source that
// publishes none (Chrome for Testing).
func (f *fetcher) sha256(ctx context.Context, url string) (string, error) {
	resp, err := f.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", fmt.Errorf("GET %s: %w", url, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// checksumFor returns the sha256 a checksum file gives for name
// ("<sha256>  <name>"), or the one on its only line when name is empty.
func checksumFor(text, name string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		file := ""
		if len(fields) > 1 {
			file = strings.TrimPrefix(strings.TrimPrefix(fields[len(fields)-1], "*"), "./")
		}
		if name == "" || file == name {
			if !hexSHA256.MatchString(fields[0]) {
				return "", fmt.Errorf("checksum for %q is not a sha256: %q", name, fields[0])
			}
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum for %q", name)
}

// archs are the image's architectures, by their versions.env suffix.
var archs = []string{"amd64", "arm64"}

// --- Go: https://go.dev/dl/?mode=json&include=all

func (f *fetcher) golang(ctx context.Context, current string, minor bool) (*release, error) {
	var releases []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
		Files   []struct {
			Filename string `json:"filename"`
			OS       string `json:"os"`
			Arch     string `json:"arch"`
			Kind     string `json:"kind"`
			SHA256   string `json:"sha256"`
		} `json:"files"`
	}
	if err := f.json(ctx, "https://go.dev/dl/?mode=json&include=all", &releases); err != nil {
		return nil, err
	}
	cur, _ := parseVersion(current)
	depth := 2
	if minor {
		depth = 1
	}
	var best *release
	var bestV version
	for _, r := range releases {
		v, ok := parseVersion(strings.TrimPrefix(r.Version, "go"))
		if !r.Stable || !ok || !sameLine(cur, v, depth) || (best != nil && v.compare(bestV) <= 0) {
			continue
		}
		rel := &release{version: strings.TrimPrefix(r.Version, "go"), values: map[string]string{"GO_VERSION": strings.TrimPrefix(r.Version, "go")}}
		for _, file := range r.Files {
			if file.OS == "linux" && file.Kind == "archive" && slices.Contains(archs, file.Arch) {
				rel.values["GO_SHA256_"+file.Arch] = file.SHA256
			}
		}
		if len(rel.values) == 1+len(archs) {
			best, bestV = rel, v
		}
	}
	if best == nil {
		return nil, fmt.Errorf("go: no stable release on the %s line", current)
	}
	return best, nil
}

// --- Node.js: https://nodejs.org/dist/index.json, SHASUMS256.txt

func (f *fetcher) node(ctx context.Context, current string, cutoff time.Time) (*release, error) {
	var releases []struct {
		Version string `json:"version"`
		Date    string `json:"date"`
	}
	if err := f.json(ctx, "https://nodejs.org/dist/index.json", &releases); err != nil {
		return nil, err
	}
	cur, _ := parseVersion(current)
	var bestName string
	var bestV version
	for _, r := range releases {
		v, ok := parseVersion(r.Version)
		date, err := time.Parse(time.DateOnly, r.Date)
		if !ok || err != nil || !sameLine(cur, v, 1) || date.After(cutoff) || (bestName != "" && v.compare(bestV) <= 0) {
			continue
		}
		bestName, bestV = strings.TrimPrefix(r.Version, "v"), v
	}
	if bestName == "" {
		return nil, fmt.Errorf("node: no release on the %s line before %s", current, cutoff.Format(time.DateOnly))
	}
	sums, err := f.text(ctx, "https://nodejs.org/dist/v"+bestName+"/SHASUMS256.txt")
	if err != nil {
		return nil, err
	}
	rel := &release{version: bestName, values: map[string]string{"NODE_VERSION": bestName}}
	for _, a := range archs {
		name := map[string]string{"amd64": "x64", "arm64": "arm64"}[a]
		sum, err := checksumFor(sums, "node-v"+bestName+"-linux-"+name+".tar.xz")
		if err != nil {
			return nil, fmt.Errorf("node %s: %w", bestName, err)
		}
		rel.values["NODE_SHA256_"+a] = sum
	}
	return rel, nil
}

// --- GitHub releases, checked by the digests GitHub records for each asset

type githubTool struct {
	key, repo, tagPrefix string
	// asset names the release asset for an architecture (amd64, arm64).
	asset func(version, arch string) string
}

var githubTools = []githubTool{
	{"BUN", "oven-sh/bun", "bun-v", func(_, a string) string {
		return "bun-linux-" + map[string]string{"amd64": "x64", "arm64": "aarch64"}[a] + ".zip"
	}},
	{"JJ", "jj-vcs/jj", "v", func(v, a string) string {
		return "jj-v" + v + "-" + map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[a] + "-unknown-linux-musl.tar.gz"
	}},
	{"TASK", "go-task/task", "v", func(_, a string) string { return "task_linux_" + a + ".tar.gz" }},
	{"GH", "cli/cli", "v", func(v, a string) string { return "gh_" + v + "_linux_" + a + ".tar.gz" }},
	{"RIPGREP", "BurntSushi/ripgrep", "", func(v, a string) string {
		return "ripgrep-" + v + "-" + map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[a] + "-unknown-linux-musl.tar.gz"
	}},
	{"UV", "astral-sh/uv", "", func(_, a string) string {
		return "uv-" + map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[a] + "-unknown-linux-gnu.tar.gz"
	}},
	{"AGENT_BROWSER", "vercel-labs/agent-browser", "v", func(_, a string) string {
		return "agent-browser-linux-" + map[string]string{"amd64": "x64", "arm64": "arm64"}[a]
	}},
}

func (f *fetcher) github(ctx context.Context, t githubTool, cutoff time.Time) (*release, error) {
	var releases []struct {
		TagName     string    `json:"tag_name"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := f.json(ctx, "https://api.github.com/repos/"+t.repo+"/releases?per_page=50", &releases); err != nil {
		return nil, err
	}
	var best *release
	var bestV version
	for _, r := range releases {
		name, ok := strings.CutPrefix(r.TagName, t.tagPrefix)
		v, vok := parseVersion(name)
		if !ok || !vok || r.Draft || r.Prerelease || r.PublishedAt.After(cutoff) ||
			strings.ContainsAny(name, "-+") || (best != nil && v.compare(bestV) <= 0) {
			continue
		}
		rel := &release{version: name, values: map[string]string{t.key + "_VERSION": name}}
		for _, a := range archs {
			want := t.asset(name, a)
			for _, asset := range r.Assets {
				if sum, ok := strings.CutPrefix(asset.Digest, "sha256:"); ok && asset.Name == want && hexSHA256.MatchString(sum) {
					rel.values[t.key+"_SHA256_"+a] = sum
				}
			}
		}
		if len(rel.values) == 1+len(archs) {
			best, bestV = rel, v
		}
	}
	if best == nil {
		return nil, fmt.Errorf("%s: no release with digests for both architectures before %s", t.repo, cutoff.Format(time.DateOnly))
	}
	return best, nil
}

// --- Docker Engine: Docker's apt repository for the base's Ubuntu release

// dockerKeys are the versions.env keys of Docker's packages, which are apt
// versions such as 5:29.9.2-1~ubuntu.26.04~resolute.
var dockerKeys = []string{"DOCKER_CE_VERSION", "CONTAINERD_VERSION", "DOCKER_BUILDX_VERSION", "DOCKER_COMPOSE_VERSION"}

var dockerPackages = map[string]string{
	"docker-ce":             "DOCKER_CE_VERSION",
	"containerd.io":         "CONTAINERD_VERSION",
	"docker-buildx-plugin":  "DOCKER_BUILDX_VERSION",
	"docker-compose-plugin": "DOCKER_COMPOSE_VERSION",
}

// upstream is the upstream part of an apt version: no epoch, no revision.
func upstream(v string) string {
	if _, after, ok := strings.Cut(v, ":"); ok {
		v = after
	}
	if i := strings.LastIndex(v, "-"); i > 0 {
		v = v[:i]
	}
	return v
}

// compareApt orders the apt versions Docker's repository uses: by upstream
// version, then by the revision's leading number (2.4.2-3 after 2.4.2-1).
func compareApt(a, b string) int {
	ua, _ := parseVersion(upstream(a))
	ub, _ := parseVersion(upstream(b))
	if c := ua.compare(ub); c != 0 {
		return c
	}
	return revision(a) - revision(b)
}

func revision(v string) int {
	i := strings.LastIndex(v, "-")
	if i < 0 {
		return 0
	}
	n := 0
	for _, c := range v[i+1:] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// docker reads the repository's package index for each architecture and
// takes each package's newest version that both carry. apt checks the
// packages against the repository's signing key when the image is built.
func (f *fetcher) docker(ctx context.Context, codename string) (*release, error) {
	var common map[string][]string
	for _, a := range archs {
		text, err := f.text(ctx, "https://download.docker.com/linux/ubuntu/dists/"+codename+"/stable/binary-"+a+"/Packages")
		if err != nil {
			return nil, err
		}
		versions := map[string][]string{}
		var pkg string
		for _, line := range strings.Split(text, "\n") {
			if p, ok := strings.CutPrefix(line, "Package: "); ok {
				pkg = p
			} else if v, ok := strings.CutPrefix(line, "Version: "); ok && dockerPackages[pkg] != "" {
				versions[pkg] = append(versions[pkg], v)
			}
		}
		if common == nil {
			common = versions
			continue
		}
		for p, vs := range common {
			common[p] = slices.DeleteFunc(vs, func(v string) bool { return !slices.Contains(versions[p], v) })
		}
	}
	rel := &release{values: map[string]string{}}
	for p, key := range dockerPackages {
		var best string
		for _, v := range common[p] {
			if _, ok := parseVersion(upstream(v)); ok && (best == "" || compareApt(v, best) > 0) {
				best = v
			}
		}
		if best == "" {
			return nil, fmt.Errorf("docker: no %s for both architectures in %s", p, codename)
		}
		rel.values[key] = best
	}
	rel.version = rel.values["DOCKER_CE_VERSION"]
	return rel, nil
}

// --- rustup and Rust: https://static.rust-lang.org

var (
	tomlVersion = regexp.MustCompile(`(?m)^version = ["']([0-9][^"' ]*)`)
	tomlDate    = regexp.MustCompile(`(?m)^date = ["']([0-9-]+)["']`)
)

func rustTarget(arch string) string {
	return map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch] + "-unknown-linux-gnu"
}

func (f *fetcher) rustup(ctx context.Context) (*release, error) {
	text, err := f.text(ctx, "https://static.rust-lang.org/rustup/release-stable.toml")
	if err != nil {
		return nil, err
	}
	m := tomlVersion.FindStringSubmatch(text)
	if m == nil {
		return nil, errors.New("rustup: no version in release-stable.toml")
	}
	rel := &release{version: m[1], values: map[string]string{"RUSTUP_VERSION": m[1]}}
	for _, a := range archs {
		sums, err := f.text(ctx, "https://static.rust-lang.org/rustup/archive/"+m[1]+"/"+rustTarget(a)+"/rustup-init.sha256")
		if err != nil {
			return nil, err
		}
		if rel.values["RUSTUP_SHA256_"+a], err = checksumFor(sums, ""); err != nil {
			return nil, fmt.Errorf("rustup %s: %w", m[1], err)
		}
	}
	return rel, nil
}

func (f *fetcher) rust(ctx context.Context, cutoff time.Time) (*release, error) {
	text, err := f.text(ctx, "https://static.rust-lang.org/dist/channel-rust-stable.toml")
	if err != nil {
		return nil, err
	}
	d := tomlDate.FindStringSubmatch(text)
	_, pkg, _ := strings.Cut(text, "\n[pkg.rust]\n")
	v := tomlVersion.FindStringSubmatch(pkg)
	if d == nil || v == nil {
		return nil, errors.New("rust: no date or rust version in channel-rust-stable.toml")
	}
	if date, err := time.Parse(time.DateOnly, d[1]); err != nil || date.After(cutoff) {
		return nil, nil // the current stable is too new; keep the pin
	}
	sums, err := f.text(ctx, "https://static.rust-lang.org/dist/channel-rust-"+v[1]+".toml.sha256")
	if err != nil {
		return nil, err
	}
	sum, err := checksumFor(sums, "")
	if err != nil {
		return nil, fmt.Errorf("rust %s: %w", v[1], err)
	}
	return &release{version: v[1], values: map[string]string{"RUST_VERSION": v[1], "RUST_MANIFEST_SHA256": sum}}, nil
}

// --- Playwright and the Chrome for Testing headless shell it names

func (f *fetcher) playwright(ctx context.Context, current string, cutoff time.Time) (*release, error) {
	var pkg struct {
		Time     map[string]string `json:"time"`
		Versions map[string]struct {
			Dist struct {
				Tarball string `json:"tarball"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if err := f.json(ctx, "https://registry.npmjs.org/playwright-core", &pkg); err != nil {
		return nil, err
	}
	var bestName string
	var bestV version
	for name := range pkg.Versions {
		v, ok := parseVersion(name)
		published, err := time.Parse(time.RFC3339, pkg.Time[name])
		if !ok || err != nil || strings.ContainsAny(name, "-+") || published.After(cutoff) ||
			(bestName != "" && v.compare(bestV) <= 0) {
			continue
		}
		bestName, bestV = name, v
	}
	if bestName == "" {
		return nil, errors.New("playwright-core: no release before " + cutoff.Format(time.DateOnly))
	}
	if bestName == current {
		return &release{version: current}, nil
	}
	revision, chrome, err := f.headlessShell(ctx, pkg.Versions[bestName].Dist.Tarball)
	if err != nil {
		return nil, fmt.Errorf("playwright-core %s: %w", bestName, err)
	}
	rel := &release{version: bestName, values: map[string]string{
		"PLAYWRIGHT_VERSION": bestName,
		"CHROME_REVISION":    revision,
		"CHROME_VERSION":     chrome,
	}}
	for _, a := range archs {
		platform := map[string]string{"amd64": "linux64", "arm64": "linux-arm64"}[a]
		url := "https://storage.googleapis.com/chrome-for-testing-public/" + chrome + "/" + platform + "/chrome-headless-shell-" + platform + ".zip"
		if rel.values["CHROME_SHA256_"+a], err = f.sha256(ctx, url); err != nil {
			return nil, err
		}
	}
	return rel, nil
}

// headlessShell reads browsers.json from a playwright-core tarball and
// returns the revision and Chrome version of its chromium-headless-shell.
func (f *fetcher) headlessShell(ctx context.Context, tarball string) (revision, chrome string, err error) {
	resp, err := f.get(ctx, tarball)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", "", err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err != nil {
			return "", "", fmt.Errorf("no package/browsers.json: %w", err)
		}
		if h.Name != "package/browsers.json" {
			continue
		}
		var browsers struct {
			Browsers []struct {
				Name           string `json:"name"`
				Revision       string `json:"revision"`
				BrowserVersion string `json:"browserVersion"`
			} `json:"browsers"`
		}
		if err := json.NewDecoder(tr).Decode(&browsers); err != nil {
			return "", "", err
		}
		for _, b := range browsers.Browsers {
			if b.Name == "chromium-headless-shell" && b.Revision != "" && b.BrowserVersion != "" {
				return b.Revision, b.BrowserVersion, nil
			}
		}
		return "", "", errors.New("browsers.json names no chromium-headless-shell")
	}
}

// --- The base image and the apt snapshot

func ubuntuDigest(ctx context.Context, tag string) (string, error) {
	ref, err := name.ParseReference("index.docker.io/library/ubuntu:" + tag)
	if err != nil {
		return "", err
	}
	desc, err := remote.Head(ref, remote.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("ubuntu:%s: %w", tag, err)
	}
	return desc.Digest.String(), nil
}

// aptSnapshot is the snapshot.ubuntu.com timestamp of the start of t's day.
func aptSnapshot(t time.Time) string {
	return t.UTC().Truncate(24 * time.Hour).Format("20060102T150405Z")
}
