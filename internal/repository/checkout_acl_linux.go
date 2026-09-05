package repository

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func rejectCheckoutDirectoryACL(directory *os.File) error {
	for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		size, err := unix.Fgetxattr(int(directory.Fd()), name, nil)
		if errors.Is(err, unix.ENODATA) {
			continue
		}
		if err != nil {
			return err
		}
		if size != 0 {
			return fmt.Errorf("directory has an unsupported ACL")
		}
	}
	return nil
}

func rejectCheckoutDirectoryFlags(directory *os.File) error {
	// Linux ioctl_getflags writes unsigned int despite the legacy long-sized
	// request encoding (fs/ioctl.c); use the explicit uint32 x/sys wrapper.
	flags, err := unix.IoctlGetUint32(int(directory.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil {
		return err
	}
	// Kernel storage layout only: FS_INDEX_FL, FS_EXTENT_FL, FS_INLINE_DATA_FL
	// from include/uapi/linux/fs.h. All policy, permission and unknown bits fail.
	const layout = 0x00001000 | 0x00080000 | 0x10000000
	if flags & ^uint32(layout) != 0 {
		return fmt.Errorf("directory has unsupported inode flags")
	}
	return nil
}
