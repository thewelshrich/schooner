package repository

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

func rejectCheckoutDirectoryACL(directory *os.File) error {
	// fgetattrlist returns a length followed by an attrreference_t. An absent
	// extended-security attribute has zero data length; any payload is unsupported.
	attributes := unix.Attrlist{Bitmapcount: 5, Commonattr: unix.ATTR_CMN_EXTENDED_SECURITY}
	var result [3]uint32
	_, _, errno := unix.Syscall6(unix.SYS_FGETATTRLIST, directory.Fd(), uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(&result[0])), unsafe.Sizeof(result), 0, 0)
	runtime.KeepAlive(directory)
	if errno != 0 {
		return errno
	}
	if result[0] != uint32(unsafe.Sizeof(result)) || result[1] != 8 || result[2] != 0 {
		return fmt.Errorf("directory has an unsupported ACL")
	}
	return nil
}
