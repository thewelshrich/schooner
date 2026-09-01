package client

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestDiagnoseReportsClientReadinessWithoutBoxChecks(t *testing.T) {
	environment := environment{
		operatingSystem: "darwin",
		architecture:    "arm64",
		lookPath:        func(name string) (string, error) { return "/usr/bin/" + name, nil },
		run: func(_ context.Context, executable string, _ ...string) (string, error) {
			switch executable {
			case "/usr/bin/sw_vers":
				return "15.6.1\n", nil
			case "/usr/bin/ssh":
				return "OpenSSH_9.9p2, LibreSSL 3.3.6\n", nil
			case "/usr/bin/git":
				return "git version 2.50.1\n", nil
			default:
				return "", fmt.Errorf("unexpected executable %s", executable)
			}
		},
	}
	report, err := environment.diagnose(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Healthy || report.Scope != "client" {
		t.Fatalf("report = %+v", report)
	}
	wantIDs := []string{"client_platform", "openssh", "git", "ssh_keygen"}
	gotIDs := make([]string, 0, len(report.Checks))
	for _, check := range report.Checks {
		gotIDs = append(gotIDs, check.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("check IDs = %v, want %v", gotIDs, wantIDs)
	}
}

func TestDiagnoseRejectsUnsupportedMacOSVersion(t *testing.T) {
	environment := environment{
		operatingSystem: "darwin",
		architecture:    "amd64",
		lookPath:        func(name string) (string, error) { return "/usr/bin/" + name, nil },
		run: func(_ context.Context, executable string, _ ...string) (string, error) {
			if executable == "/usr/bin/sw_vers" {
				return "12.7.6\n", nil
			}
			return "available", nil
		},
	}
	report, err := environment.diagnose(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy || report.Checks[0].OK || report.Checks[0].Message != "Client platform is unsupported: macOS 12.7.6/amd64 (macOS 13 or later is required)." {
		t.Fatalf("report = %+v", report)
	}
}

func TestDiagnoseReportsMissingClientPrerequisite(t *testing.T) {
	environment := environment{
		operatingSystem: "linux",
		architecture:    "amd64",
		lookPath: func(name string) (string, error) {
			if name == "ssh" {
				return "", errors.New("missing")
			}
			return "/usr/bin/git", nil
		},
		run: func(context.Context, string, ...string) (string, error) { return "git version 2.50.1", nil },
	}
	report, err := environment.diagnose(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy || report.Checks[1].OK || report.Checks[1].Message != "OpenSSH is unavailable in PATH." {
		t.Fatalf("report = %+v", report)
	}
}

func TestDiagnoseReportsMissingSSHKeygen(t *testing.T) {
	environment := environment{
		operatingSystem: "linux",
		architecture:    "arm64",
		lookPath: func(name string) (string, error) {
			if name == "ssh-keygen" {
				return "", errors.New("missing")
			}
			return "/usr/bin/" + name, nil
		},
		run: func(context.Context, string, ...string) (string, error) { return "available", nil },
	}
	report, err := environment.diagnose(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy || report.Checks[3].OK || report.Checks[3].Message != "OpenSSH key generator is unavailable in PATH." {
		t.Fatalf("report = %+v", report)
	}
}

func TestDiagnoseHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (environment{}).diagnose(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
