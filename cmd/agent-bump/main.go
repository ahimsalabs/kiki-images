// Command agent-bump moves the pins in the agent image's versions.env to
// newer releases, as internal/agentbump describes, and prints what moved as
// JSON. The bump workflow (.github/workflows/bump-agent.yml) runs it.
//
//	agent-bump -versions images/agent/versions.env
//
// By default it bumps every tool to its newest release at least -cooldown
// old. With -trivy <report.json>, a Trivy JSON scan of the published
// image, it bumps only the tools that carry a vulnerability with a fix,
// without the cooldown, and reports the ones a bump cannot fix yet.
//
// With -diff <old versions.env> it bumps nothing and reports what moved from
// that file to -versions: what a pull request that several runs updated
// carries. With -summary <file> it also writes a Markdown description for
// the pull request. GITHUB_TOKEN, if set, authenticates GitHub API
// calls.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ahimsalabs/kiki-images/internal/agentbump"
)

func main() {
	var (
		versions = flag.String("versions", "images/agent/versions.env", "the versions.env to update")
		only     = flag.String("only", "", "comma-separated tools to bump (default all: "+strings.Join(agentbump.Tools, ",")+")")
		cooldown = flag.Duration("cooldown", 72*time.Hour, "how old a release must be")
		goMinor  = flag.Bool("go-minor", false, "allow a new Go minor release")
		trivy    = flag.String("trivy", "", "bump only what fixes this Trivy JSON report's fixable findings, without the cooldown")
		summary  = flag.String("summary", "", "write a Markdown summary for the pull request here")
		dryRun   = flag.Bool("dry-run", false, "print what would change, write nothing")
		diff     = flag.String("diff", "", "report what moved from this versions.env to -versions; bump nothing")
	)
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *versions, *only, *cooldown, *goMinor, *trivy, *summary, *dryRun, *diff); err != nil {
		fmt.Fprintln(os.Stderr, "agent-bump:", err)
		os.Exit(1)
	}
}

type result struct {
	Level    string              `json:"level"`
	Changes  []agentbump.Change  `json:"changes"`
	Findings []agentbump.Finding `json:"findings,omitempty"`
	// Unfixed are findings no release fixes yet.
	Unfixed []agentbump.Finding `json:"unfixed,omitempty"`
}

func run(ctx context.Context, versions, only string, cooldown time.Duration, goMinor bool, trivy, summary string, dryRun bool, diff string) error {
	f, err := agentbump.ReadEnvFile(versions)
	if err != nil {
		return err
	}
	opts := agentbump.Options{Cooldown: cooldown, GoMinor: goMinor, GitHubToken: os.Getenv("GITHUB_TOKEN")}
	if only != "" {
		opts.Only = strings.Split(only, ",")
	}
	var res result
	if trivy != "" {
		r, err := os.Open(trivy)
		if err != nil {
			return err
		}
		res.Findings, err = agentbump.ReadTrivy(r)
		r.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", trivy, err)
		}
		opts.Only, opts.Cooldown = agentbump.FindingTools(res.Findings), 0
	}
	if diff != "" {
		old, err := agentbump.ReadEnvFile(diff)
		if err != nil {
			return err
		}
		res.Changes = agentbump.Diff(old, f)
		res.Unfixed = agentbump.Unfixed(res.Findings, res.Changes)
		res.Level = agentbump.Highest(res.Changes).String()
		return emit(res, summary)
	}
	if trivy != "" && len(opts.Only) == 0 {
		return emit(res, summary) // nothing fixable
	}
	if res.Changes, err = agentbump.Bump(ctx, f, opts); err != nil {
		return err
	}
	res.Unfixed = agentbump.Unfixed(res.Findings, res.Changes)
	res.Level = agentbump.Highest(res.Changes).String()
	if !dryRun && len(res.Changes) > 0 {
		if err := f.Write(versions); err != nil {
			return err
		}
	}
	return emit(res, summary)
}

func emit(res result, summary string) error {
	if res.Level == "" {
		res.Level = agentbump.None.String()
	}
	if summary != "" {
		if err := os.WriteFile(summary, []byte(markdown(res)), 0o644); err != nil {
			return err
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

func markdown(res result) string {
	var b strings.Builder
	if len(res.Changes) > 0 {
		b.WriteString(agentbump.Markdown(res.Changes))
		fmt.Fprintf(&b, "\nLargest change: **%s**. A patch-level bump merges itself once `Build agent image` passes; anything larger waits for a review.\n", res.Level)
	}
	if len(res.Findings) > 0 {
		fmt.Fprintf(&b, "\n### Vulnerabilities this bump addresses\n\nThe nightly Trivy scan of `kiki-src/agent:latest` found fixable HIGH or CRITICAL vulnerabilities:\n\n| Pin | ID | Package | Severity | Fixed in |\n| --- | --- | --- | --- | --- |\n")
		for _, f := range res.Findings {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", f.Tool, f.ID, f.Package, f.Severity, f.Fixed)
		}
	}
	if len(res.Unfixed) > 0 {
		fmt.Fprintf(&b, "\n%d of them wait on an upstream release (no newer release of their tool yet).\n", len(res.Unfixed))
	}
	return b.String()
}
