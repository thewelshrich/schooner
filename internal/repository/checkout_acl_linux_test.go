package repository

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"
)

func installCheckoutTestACL(t *testing.T, path string) {
	t.Helper()
	// A default ACL is extended directory metadata even with only base entries.
	acl := make([]byte, 4+3*8)
	binary.LittleEndian.PutUint32(acl, 2)
	for i, tag := range []uint16{1, 4, 32} {
		entry := acl[4+i*8:]
		binary.LittleEndian.PutUint16(entry, tag)
		binary.LittleEndian.PutUint16(entry[2:], 7)
		binary.LittleEndian.PutUint32(entry[4:], ^uint32(0))
	}
	if err := unix.Setxattr(path, "system.posix_acl_default", acl, 0); err != nil {
		t.Fatalf("set directory ACL: %v", err)
	}
}
