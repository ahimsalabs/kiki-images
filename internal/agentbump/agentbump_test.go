package agentbump

import (
	"strings"
	"testing"
	"time"
)

const sample = `# comment
UBUNTU_VERSION=26.04

# Go
GO_VERSION=1.27.3
GO_SHA256_amd64=aa
`

func TestEnvFileSetKeepsTheRest(t *testing.T) {
	f := ParseEnvFile(sample)
	if v, _ := f.Get("GO_VERSION"); v != "1.27.3" {
		t.Fatalf("GO_VERSION = %q", v)
	}
	if err := f.Set("GO_VERSION", "1.27.4"); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(sample, "1.27.3", "1.27.4", 1)
	if got := f.String(); got != want {
		t.Errorf("after Set:\n%s\nwant:\n%s", got, want)
	}
	if err := f.Set("NOPE", "x"); err == nil {
		t.Error("Set of an unset key succeeded")
	}
}

func TestLevelOf(t *testing.T) {
	for _, tc := range []struct {
		old, new string
		want     Level
	}{
		{"1.27.3", "1.27.3", None},
		{"1.27.3", "1.27.4", Patch},
		{"1.27.3", "1.28.0", Minor},
		{"24.22.2", "26.0.0", Major},
		{"0.46.0", "0.47.0", Minor},
		{"0.13.6", "0.13.7", Patch},
	} {
		if got := levelOf(tc.old, tc.new); got != tc.want {
			t.Errorf("levelOf(%s, %s) = %v, want %v", tc.old, tc.new, got, tc.want)
		}
	}
}

func TestSameLine(t *testing.T) {
	v := func(s string) version { x, _ := parseVersion(s); return x }
	if !sameLine(v("1.27.3"), v("1.27.9"), 2) || sameLine(v("1.27.3"), v("1.28.0"), 2) {
		t.Error("Go's minor line")
	}
	if !sameLine(v("24.22.2"), v("24.30.0"), 1) || sameLine(v("24.22.2"), v("26.0.0"), 1) {
		t.Error("Node's major line")
	}
}

func TestChecksumFor(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	text := sum + "  node-v24.22.2-linux-x64.tar.xz\n" + strings.Repeat("cd", 32) + "  node-v24.22.2-linux-arm64.tar.xz\n"
	if got, err := checksumFor(text, "node-v24.22.2-linux-x64.tar.xz"); err != nil || got != sum {
		t.Errorf("named: %q, %v", got, err)
	}
	if got, err := checksumFor(sum+" *./rustup-init\n", ""); err != nil || got != sum {
		t.Errorf("only line: %q, %v", got, err)
	}
	if _, err := checksumFor(text, "missing"); err == nil {
		t.Error("missing name: no error")
	}
	if _, err := checksumFor("nothex  x\n", "x"); err == nil {
		t.Error("not a sha256: no error")
	}
}

func TestApplyNeverMovesBackwards(t *testing.T) {
	f := ParseEnvFile(sample)
	c, err := apply(f, "GO", "GO_VERSION", &release{version: "1.27.2", values: map[string]string{"GO_VERSION": "1.27.2"}}, None)
	if err != nil || c != nil {
		t.Fatalf("apply older = %v, %v", c, err)
	}
	c, err = apply(f, "GO", "GO_VERSION", &release{version: "1.27.4", values: map[string]string{"GO_VERSION": "1.27.4", "GO_SHA256_amd64": "bb"}}, None)
	if err != nil || c == nil || c.Level != Patch {
		t.Fatalf("apply newer = %+v, %v", c, err)
	}
	if v, _ := f.Get("GO_SHA256_amd64"); v != "bb" {
		t.Errorf("GO_SHA256_amd64 = %q", v)
	}
}

func TestApplyFloorCapsRust(t *testing.T) {
	f := ParseEnvFile("RUST_VERSION=1.99.1\nRUST_MANIFEST_SHA256=aa\n")
	c, err := apply(f, "RUST", "RUST_VERSION", &release{version: "1.100.0", values: map[string]string{"RUST_VERSION": "1.100.0", "RUST_MANIFEST_SHA256": "bb"}}, -1)
	if err != nil || c == nil || c.Level != Patch {
		t.Fatalf("rust minor = %+v, %v", c, err)
	}
}

func TestReadTrivy(t *testing.T) {
	report := `{"Results":[
	  {"Target":"agent (ubuntu 26.04)","Class":"os-pkgs","Vulnerabilities":[
	    {"VulnerabilityID":"CVE-1","PkgName":"openssl","FixedVersion":"3.5.6","Severity":"HIGH"},
	    {"VulnerabilityID":"CVE-2","PkgName":"zlib","FixedVersion":"","Severity":"CRITICAL"},
	    {"VulnerabilityID":"CVE-6","PkgName":"linux-libc-dev","FixedVersion":"7.0.0-43.43","Severity":"HIGH"},
	    {"VulnerabilityID":"CVE-7","PkgName":"containerd.io","FixedVersion":"2.4.3-1","Severity":"HIGH"}]},
	  {"Target":"usr/local/gh/bin/gh","Class":"lang-pkgs","Vulnerabilities":[
	    {"VulnerabilityID":"CVE-3","PkgName":"stdlib","FixedVersion":"1.27.4","Severity":"HIGH"}]},
	  {"Target":"usr/bin/dockerd","Class":"lang-pkgs","Vulnerabilities":[
	    {"VulnerabilityID":"CVE-4","PkgName":"stdlib","FixedVersion":"1.27.4","Severity":"HIGH"}]},
	  {"Target":"Node.js","Class":"lang-pkgs","Vulnerabilities":[
	    {"VulnerabilityID":"CVE-5","PkgName":"cross-spawn","PkgPath":"usr/local/node/lib/node_modules/npm/node_modules/cross-spawn/package.json","FixedVersion":"7.0.5","Severity":"HIGH"}]}
	]}`
	findings, err := ReadTrivy(strings.NewReader(report))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range findings {
		got = append(got, f.ID+":"+f.Tool)
	}
	if want := "CVE-1:APT CVE-7:DOCKER CVE-3:GH CVE-4:DOCKER CVE-5:NODE"; strings.Join(got, " ") != want {
		t.Errorf("findings %v, want %s", got, want)
	}
	if tools := strings.Join(FindingTools(findings), ","); tools != "APT,DOCKER,NODE,GH" {
		t.Errorf("FindingTools = %s", tools)
	}
	unfixed := Unfixed(findings, []Change{{Tool: "APT"}, {Tool: "DOCKER"}, {Tool: "NODE"}})
	if len(unfixed) != 1 || unfixed[0].Tool != "GH" {
		t.Errorf("Unfixed = %+v", unfixed)
	}
}

func TestAptSnapshot(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-11-03T17:04:05+02:00")
	if got := aptSnapshot(now); got != "20261103T000000Z" {
		t.Errorf("aptSnapshot = %s", got)
	}
}

func TestDiff(t *testing.T) {
	old := ParseEnvFile("UBUNTU_VERSION=26.04\nUBUNTU_DIGEST=sha256:aaaa\nAPT_SNAPSHOT=20261101T000000Z\nGO_VERSION=1.27.3\nNODE_VERSION=24.22.2\nRUST_VERSION=1.99.1\n")
	new := ParseEnvFile("UBUNTU_VERSION=26.04\nUBUNTU_DIGEST=sha256:aaaa\nAPT_SNAPSHOT=20261102T000000Z\nGO_VERSION=1.28.0\nNODE_VERSION=24.22.2\nRUST_VERSION=1.100.0\n")
	changes := Diff(old, new)
	var got []string
	for _, c := range changes {
		got = append(got, c.Tool+":"+c.LevelName)
	}
	if want := "APT:patch GO:minor RUST:patch"; strings.Join(got, " ") != want {
		t.Errorf("Diff = %v, want %s", got, want)
	}
	if Highest(changes) != Minor {
		t.Errorf("Highest = %v", Highest(changes))
	}
}

func TestUpstream(t *testing.T) {
	for in, want := range map[string]string{
		"5:29.9.2-1~ubuntu.26.04~resolute": "29.9.2",
		"2.4.2-3~ubuntu.26.04~resolute":    "2.4.2",
		"0.38.1":                           "0.38.1",
	} {
		if got := upstream(in); got != want {
			t.Errorf("upstream(%q) = %q, want %q", in, got, want)
		}
	}
	if compareApt("2.4.2-3~ubuntu.26.04~resolute", "2.4.2-1~ubuntu.26.04~resolute") <= 0 ||
		compareApt("5:29.10.0-1~x", "5:29.9.2-1~x") <= 0 {
		t.Error("compareApt")
	}
	if levelOf(upstream("5:29.9.2-1~x"), upstream("5:29.10.0-1~x")) != Minor {
		t.Error("docker minor")
	}
}
