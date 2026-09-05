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
