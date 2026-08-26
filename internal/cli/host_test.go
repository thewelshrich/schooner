package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
)

func TestDecodeInteractiveHostRequestAcceptsStandardPaddedBase64(t *testing.T) {
	want := hostruntime.NewWorktreeShellRequest("/home/alice/worktrees", "11111111-1111-4111-8111-111111111111", "repo")
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got hostruntime.WorktreeShellRequest
	if err = decodeInteractiveHostRequest(base64.StdEncoding.EncodeToString(payload), &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("request = %+v, want %+v", got, want)
	}
}

func TestWriteDoctorResultReturnsFailureAfterUnhealthyReport(t *testing.T) {
	for _, output := range []string{"human", "json"} {
		t.Run(output, func(t *testing.T) {
			var destination bytes.Buffer
			report := hostruntime.DoctorReport{SchemaVersion: hostruntime.SchemaVersion, ProtocolVersion: hostruntime.ProtocolVersion, Healthy: false}
			err := writeDoctorResult(&destination, output, report, nil)
			var status exitStatusError
			if !errors.As(err, &status) || status.code != exitFailure {
				t.Fatalf("error=%v status=%+v", err, status)
			}
			if strings.TrimSpace(destination.String()) == "" {
				t.Fatal("unhealthy report was not written")
			}
		})
	}
}

func TestWriteDoctorResultReturnsSuccessForHealthyReport(t *testing.T) {
	var destination bytes.Buffer
	report := hostruntime.DoctorReport{SchemaVersion: hostruntime.SchemaVersion, ProtocolVersion: hostruntime.ProtocolVersion, Healthy: true}
	if err := writeDoctorResult(&destination, "human", report, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorExplainsUnsupportedLocalClientCanUseRemoteBoxes(t *testing.T) {
	var destination bytes.Buffer
	report := hostruntime.DoctorReport{
		SchemaVersion:   hostruntime.SchemaVersion,
		ProtocolVersion: hostruntime.ProtocolVersion,
		Healthy:         false,
		Checks:          []hostruntime.Check{{ID: "platform", OK: false, Message: "Platform is darwin/arm64."}},
	}
	_ = writeDoctorResult(&destination, "human", report, nil)
	if !strings.Contains(destination.String(), "This client can still manage remote boxes") || !strings.Contains(destination.String(), "schooner box add") {
		t.Fatalf("doctor output = %q", destination.String())
	}
}
