// Package client owns readiness checks for the local Schooner client.
package client

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/thewelshrich/schooner/internal/process"
)

const (
	SchemaVersion   = "2"
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
	platform, err := e.checkPlatform(ctx)
	if err != nil {
		return DoctorReport{}, err
	}
	checks := []Check{platform}
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
	checks = append(checks, e.checkExecutable("ssh_keygen", "ssh-keygen", "OpenSSH key generator"))
	healthy := true
	for _, check := range checks {
		healthy = healthy && check.OK
	}
	return DoctorReport{SchemaVersion: SchemaVersion, Scope: "client", Healthy: healthy, Checks: checks}, nil
}

func (e environment) checkPlatform(ctx context.Context) (Check, error) {
	architectureOK := e.architecture == "amd64" || e.architecture == "arm64"
	switch e.operatingSystem {
	case "linux":
		return Check{ID: "client_platform", OK: architectureOK, Message: platformMessage(architectureOK, "Linux/"+e.architecture, "")}, nil
	case "darwin":
		path, err := e.lookPath("sw_vers")
		if err != nil {
			return Check{ID: "client_platform", Message: platformMessage(false, "macOS/"+e.architecture, "could not determine the macOS version")}, nil
		}
		version, err := e.run(ctx, path, "-productVersion")
		if ctx.Err() != nil {
			return Check{}, ctx.Err()
		}
		version = firstLine(version)
		major, parseErr := macOSMajorVersion(version)
		if err != nil || parseErr != nil {
			return Check{ID: "client_platform", Message: platformMessage(false, "macOS/"+e.architecture, "could not determine the macOS version")}, nil
		}
		ok := architectureOK && major >= 13
		reason := ""
		if major < 13 {
			reason = "macOS 13 or later is required"
		}
		return Check{ID: "client_platform", OK: ok, Message: platformMessage(ok, "macOS "+version+"/"+e.architecture, reason)}, nil
	default:
		return Check{ID: "client_platform", Message: platformMessage(false, e.operatingSystem+"/"+e.architecture, "")}, nil
	}
}

func macOSMajorVersion(version string) (int, error) {
	major, _, _ := strings.Cut(version, ".")
	return strconv.Atoi(major)
}

func (e environment) checkExecutable(id, name, display string) Check {
	if _, err := e.lookPath(name); err != nil {
		return Check{ID: id, Message: fmt.Sprintf("%s is unavailable in PATH.", display)}
	}
	return Check{ID: id, OK: true, Message: fmt.Sprintf("%s is available.", display)}
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

func platformMessage(ok bool, platform, reason string) string {
	if ok {
		return "Client platform is supported: " + platform + "."
	}
	if reason != "" {
		return "Client platform is unsupported: " + platform + " (" + reason + ")."
	}
	return "Client platform is unsupported: " + platform + "."
}
