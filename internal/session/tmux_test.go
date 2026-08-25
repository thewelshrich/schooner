package session

import (
	"reflect"
	"testing"
)

func TestParseManagedSessionsRequiresVersionedExactMetadata(t *testing.T) {
	output := []byte("1\tsession-1\t/work/repo\n1\tsession-2\t/work/other\n\tplain\t/work/repo\n2\tfuture\t/work/repo\n1\tbad\rid\t/work/repo\n")
	if got, want := parseManagedSessions(output, "/work/repo"), []string{"session-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sessions = %v, want %v", got, want)
	}
}
