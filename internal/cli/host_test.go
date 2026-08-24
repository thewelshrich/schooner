package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	hostruntime "github.com/thewelshrich/schooner/internal/runtime"
)

func TestWriteDoctorResultReturnsFailureAfterUnhealthyReport(t *testing.T) {
	for _, output := range []string{"human", "json"} {
		t.Run(output, func(t *testing.T) {
			var destination bytes.Buffer
			report := hostruntime.DoctorReport{SchemaVersion: hostruntime.SchemaVersion, ProtocolVersion: hostruntime.ProtocolVersion, Healthy: false}
			err := writeDoctorResult(&destination, output, report)
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
	if err := writeDoctorResult(&destination, "human", report); err != nil {
		t.Fatal(err)
	}
}
