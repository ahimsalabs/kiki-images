package agentbump

import (
	"encoding/json"
	"io"
	"slices"
	"sort"
	"strings"
)

// Finding is a vulnerability a scan found with a fix available, and the
// tool whose pin a bump would move to pick the fix up.
type Finding struct {
	Tool     string `json:"tool"`
	ID       string `json:"id"`
	Package  string `json:"package"`
	Target   string `json:"target"`
	Severity string `json:"severity"`
	Fixed    string `json:"fixed"`
}

// toolPaths map where each tool lives in the image (as Trivy names a
// target, without the leading slash) to its pin. Docker's binaries come
// from Docker's apt repository; anything else, the OS packages and the
// binaries Ubuntu's apt installs, is APT.
var toolPaths = []struct{ prefix, tool string }{
	{"usr/local/go/", "GO"},
	{"usr/local/node/", "NODE"},
	{"usr/local/bun/", "BUN"},
	{"usr/local/jj/", "JJ"},
	{"usr/local/task/", "TASK"},
	{"usr/local/gh/", "GH"},
	{"usr/local/ripgrep/", "RIPGREP"},
	{"usr/local/uv/", "UV"},
	{"usr/bin/docker", "DOCKER"},
	{"usr/bin/containerd", "DOCKER"},
	{"usr/bin/ctr", "DOCKER"},
	{"usr/bin/runc", "DOCKER"},
	{"usr/libexec/docker/", "DOCKER"},
	{"usr/local/bin/agent-browser", "AGENT_BROWSER"},
	{"opt/ms-playwright/", "PLAYWRIGHT"},
	{"usr/local/lib/kiki-agent/rustup-init", "RUSTUP"},
	{"opt/rust-dist/", "RUST"},
}

func toolFor(target string) string {
	target = strings.TrimPrefix(target, "/")
	for _, p := range toolPaths {
		if strings.HasPrefix(target, p.prefix) {
			return p.tool
		}
	}
	return "APT"
}

// ReadTrivy reads a Trivy JSON report (trivy image --format json) and
// returns its vulnerabilities that have a fixed version, each with the
// tool that carries it.
func ReadTrivy(r io.Reader) ([]Finding, error) {
	var report struct {
		Results []struct {
			Target          string `json:"Target"`
			Class           string `json:"Class"`
			Vulnerabilities []struct {
				VulnerabilityID string `json:"VulnerabilityID"`
				PkgName         string `json:"PkgName"`
				PkgPath         string `json:"PkgPath"`
				FixedVersion    string `json:"FixedVersion"`
				Severity        string `json:"Severity"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
	}
	if err := json.NewDecoder(r).Decode(&report); err != nil {
		return nil, err
	}
	var findings []Finding
	for _, res := range report.Results {
		for _, v := range res.Vulnerabilities {
			if v.FixedVersion == "" || v.PkgName == "linux-libc-dev" {
				// linux-libc-dev is the kernel's userspace headers, which
				// trackers mark with every kernel CVE; no code of it runs,
				// and kiki boots its own kernel.
				continue
			}
			tool := "APT"
			if res.Class == "os-pkgs" && dockerAptPackage(v.PkgName) {
				tool = "DOCKER"
			} else if res.Class != "os-pkgs" {
				target := res.Target
				if v.PkgPath != "" {
					target = v.PkgPath
				}
				tool = toolFor(target)
			}
			findings = append(findings, Finding{
				Tool: tool, ID: v.VulnerabilityID, Package: v.PkgName,
				Target: res.Target, Severity: v.Severity, Fixed: v.FixedVersion,
			})
		}
	}
	return findings, nil
}

// FindingTools are the tools findings name, in Tools order.
func FindingTools(findings []Finding) []string {
	var tools []string
	for _, t := range Tools {
		if slices.ContainsFunc(findings, func(f Finding) bool { return f.Tool == t }) {
			tools = append(tools, t)
		}
	}
	return tools
}

// Unfixed are the findings of tools that changes do not move: a bump finds
// no newer release of them yet.
func Unfixed(findings []Finding, changes []Change) []Finding {
	var out []Finding
	for _, f := range findings {
		if !slices.ContainsFunc(changes, func(c Change) bool { return c.Tool == f.Tool }) {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	return out
}

func dockerAptPackage(name string) bool {
	_, ok := dockerPackages[name]
	return ok || name == "docker-ce-cli"
}
