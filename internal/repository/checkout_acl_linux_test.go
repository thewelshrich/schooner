package repository

import (
	"encoding/binary"
	"os"
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

func installCheckoutTestNodump(t *testing.T, directory *os.File) {
	t.Helper()
	flags, err := unix.IoctlGetUint32(int(directory.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil {
		t.Fatal(err)
	}
	const nodump = 0x40 // FS_NODUMP_FL, include/uapi/linux/fs.h
	if err = unix.IoctlSetPointerInt(int(directory.Fd()), unix.FS_IOC_SETFLAGS, int(flags|nodump)); err != nil {
		t.Fatal(err)
	}
	current, err := unix.IoctlGetUint32(int(directory.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil || current&nodump == 0 {
		t.Fatalf("NODUMP fixture was not applied: %v", err)
	}
}
