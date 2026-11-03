// Package agentbump finds newer pins for the agent image
// (images/agent/versions.env): each tool's newest release that is old enough
// and its sha256 from where its publisher records it, the base image's
// current digest and a newer apt snapshot. cmd/agent-bump runs it for the
// bump workflow, weekly and when a scan finds a vulnerability a bump fixes.
package agentbump

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// EnvFile is versions.env: KEY=value lines among comments and blank lines,
// which Set keeps as they are.
type EnvFile struct {
	lines []string
}

var assignment = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// ReadEnvFile reads path.
func ReadEnvFile(path string) (*EnvFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseEnvFile(string(b)), nil
}

// ParseEnvFile parses the text of a versions.env.
func ParseEnvFile(text string) *EnvFile {
	return &EnvFile{lines: strings.Split(strings.TrimSuffix(text, "\n"), "\n")}
}

// Get returns the value of key, and whether the file sets it.
func (f *EnvFile) Get(key string) (string, bool) {
	for _, l := range f.lines {
		if m := assignment.FindStringSubmatch(l); m != nil && m[1] == key {
			return m[2], true
		}
	}
	return "", false
}

// MustGet returns the value of key, or an error naming it.
func (f *EnvFile) MustGet(key string) (string, error) {
	v, ok := f.Get(key)
	if !ok || v == "" {
		return "", fmt.Errorf("versions.env: %s is not set", key)
	}
	return v, nil
}

// Set replaces the value of key, which the file must already set.
func (f *EnvFile) Set(key, value string) error {
	for i, l := range f.lines {
		if m := assignment.FindStringSubmatch(l); m != nil && m[1] == key {
			f.lines[i] = key + "=" + value
			return nil
		}
	}
	return fmt.Errorf("versions.env: %s is not set", key)
}

// String returns the file's text.
func (f *EnvFile) String() string {
	return strings.Join(f.lines, "\n") + "\n"
}

// Write writes the file to path.
func (f *EnvFile) Write(path string) error {
	return os.WriteFile(path, []byte(f.String()), 0o644)
}
