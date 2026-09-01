// Package client owns readiness checks for the local Schooner client.
package client

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/thewelshrich/schooner/internal/process"
)

const (
	SchemaVersion   = "1"
	toolOutputLimit = 4 << 10
)

type Check struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type DoctorReport struct {
	SchemaVersion string  `json:"schema_version"`
	Scope         string  `json:"scope"`
	Healthy       bool    `json:"healthy"`
	Checks        []Check `json:"checks"`
}

type environment struct {
	operatingSystem string
	architecture    string
	lookPath        func(string) (string, error)
	run             func(context.Context, string, ...string) (string, error)
}

// Diagnose checks only prerequisites needed by the local client. Remote Box
// readiness remains owned by the host runtime and the live Box status flow.
func Diagnose(ctx context.Context) (DoctorReport, error) {
	environment := environment{
		operatingSystem: runtime.GOOS,
		architecture:    runtime.GOARCH,
		lookPath:        exec.LookPath,
		run:             runTool,
	}
	return environment.diagnose(ctx)
}

func (e environment) diagnose(ctx context.Context) (DoctorReport, error) {
	if err := ctx.Err(); err != nil {
		return DoctorReport{}, err
	}
	platformOK := (e.operatingSystem == "darwin" || e.operatingSystem == "linux") && (e.architecture == "amd64" || e.architecture == "arm64")
	checks := []Check{{
		ID:      "client_platform",
		OK:      platformOK,
		Message: platformMessage(platformOK, e.operatingSystem, e.architecture),
	}}
	for _, tool := range []struct {
		id, name, display string
		arguments         []string
	}{
		{id: "openssh", name: "ssh", display: "OpenSSH", arguments: []string{"-V"}},
		{id: "git", name: "git", display: "Git", arguments: []string{"--version"}},
	} {
		check, err := e.checkTool(ctx, tool.id, tool.name, tool.display, tool.arguments...)
		if err != nil {
			return DoctorReport{}, err
		}
		checks = append(checks, check)
	}
	healthy := true
	for _, check := range checks {
		healthy = healthy && check.OK
	}
	return DoctorReport{SchemaVersion: SchemaVersion, Scope: "client", Healthy: healthy, Checks: checks}, nil
}

func (e environment) checkTool(ctx context.Context, id, name, display string, arguments ...string) (Check, error) {
	path, err := e.lookPath(name)
	if err != nil {
		return Check{ID: id, Message: fmt.Sprintf("%s is unavailable in PATH.", display)}, nil
	}
	version, err := e.run(ctx, path, arguments...)
	if ctx.Err() != nil {
		return Check{}, ctx.Err()
	}
	version = firstLine(version)
	if err != nil || version == "" {
		return Check{ID: id, Message: fmt.Sprintf("%s was found but could not be run.", display)}, nil
	}
	return Check{ID: id, OK: true, Message: fmt.Sprintf("%s is available: %s.", display, strings.TrimSuffix(version, "."))}, nil
}

func runTool(ctx context.Context, executable string, arguments ...string) (string, error) {
	result, err := process.RunCapturedWithoutEnvironment(ctx, toolOutputLimit, nil, nil, executable, arguments...)
	if err != nil {
		return "", err
	}
	if result.Truncated {
		return "", fmt.Errorf("tool version output exceeds %d bytes", toolOutputLimit)
	}
	contents := append(append([]byte(nil), result.Stdout...), result.Stderr...)
	return string(contents), nil
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if line, _, found := strings.Cut(value, "\n"); found {
		return strings.TrimSpace(line)
	}
	return value
}

func platformMessage(ok bool, operatingSystem, architecture string) string {
	platform := operatingSystem + "/" + architecture
	if operatingSystem == "darwin" {
		platform = "macOS/" + architecture
	} else if operatingSystem == "linux" {
		platform = "Linux/" + architecture
	}
	if ok {
		return "Client platform is supported: " + platform + "."
	}
	return "Client platform is unsupported: " + platform + "."
}
