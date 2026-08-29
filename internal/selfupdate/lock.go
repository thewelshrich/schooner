package selfupdate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type installationLock struct {
	directory string
	token     string
}

type lockOwner struct {
	host   string
	pid    int
	target string
	token  string
}

func (u *updater) acquireLock() (*installationLock, error) {
	directory := filepath.Join(filepath.Dir(u.executablePath), lockDirectoryName)
	host, err := u.hostname()
	if err != nil || !safeLockValue(host) {
		return nil, &Error{Code: CodeLocked, Message: "local hostname is unsafe for update locking", Cause: err}
	}
	token, err := randomToken()
	if err != nil {
		return nil, &Error{Code: CodeLocked, Message: "create an update lock token", Cause: err}
	}
	if err = os.Mkdir(directory, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, &Error{Code: CodeLocked, Message: "acquire the local update lock", Cause: err}
		}
		if reclaimErr := u.reclaimLock(directory, host, token); reclaimErr != nil {
			return nil, reclaimErr
		}
		if err = os.Mkdir(directory, 0o700); err != nil {
			return nil, &Error{Code: CodeLocked, Message: "acquire the local update lock after retiring its stale owner", Cause: err}
		}
	}
	owner := lockOwner{host: host, pid: os.Getpid(), target: u.executablePath, token: token}
	if err = writeLockOwner(filepath.Join(directory, "owner"), owner); err != nil {
		_ = os.Remove(directory)
		return nil, &Error{Code: CodeLocked, Message: "publish the local update lock owner", Cause: err}
	}
	return &installationLock{directory: directory, token: token}, nil
}

func (u *updater) reclaimLock(directory, host, replacementToken string) error {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return &Error{Code: CodeLocked, Message: "installation is locked by an invalid previous operation", Cause: err}
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "owner" {
		return &Error{Code: CodeLocked, Message: "installation is locked by an ambiguous previous operation", Cause: err}
	}
	owner, err := readLockOwner(filepath.Join(directory, "owner"))
	if err != nil {
		return &Error{Code: CodeLocked, Message: "installation is locked by an ambiguous previous operation", Cause: err}
	}
	if owner.host != host || owner.target != u.executablePath {
		return &Error{Code: CodeLocked, Message: "installation is locked by an ambiguous or foreign-host operation"}
	}
	alive, err := u.processAlive(owner.pid)
	if err != nil {
		return &Error{Code: CodeLocked, Message: "installation lock owner could not be checked safely", Cause: err}
	}
	if alive {
		return &Error{Code: CodeLocked, Message: fmt.Sprintf("another Schooner installation is active for %s", u.executablePath)}
	}
	stale := directory + ".stale." + replacementToken
	if err = os.Rename(directory, stale); err != nil {
		return &Error{Code: CodeLocked, Message: "the stale installation lock changed; retry", Cause: err}
	}
	if err = os.Remove(filepath.Join(stale, "owner")); err == nil {
		err = os.Remove(stale)
	}
	if err != nil {
		return &Error{Code: CodeLocked, Message: "retire the stale installation lock", Cause: err}
	}
	return nil
}

func (l *installationLock) release() {
	if l == nil || l.directory == "" || l.token == "" {
		return
	}
	ownerPath := filepath.Join(l.directory, "owner")
	owner, err := readLockOwner(ownerPath)
	if err == nil && owner.token == l.token {
		_ = os.Remove(ownerPath)
		_ = os.Remove(l.directory)
	}
}

func writeLockOwner(path string, owner lockOwner) error {
	contents := fmt.Sprintf("schema_version=1\nhost=%s\npid=%d\ntarget=%s\ntoken=%s\n", owner.host, owner.pid, owner.target, owner.token)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.WriteString(contents); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func readLockOwner(path string) (lockOwner, error) {
	file, info, err := openRegularNoFollow(path)
	if err != nil || info.Mode().Perm()&0o022 != 0 {
		return lockOwner{}, errors.New("lock owner is unsafe")
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(contents) > 4096 {
		return lockOwner{}, errors.New("lock owner is unreadable or too large")
	}
	lines := strings.Split(string(contents), "\n")
	if len(lines) != 6 || lines[0] != "schema_version=1" || lines[5] != "" {
		return lockOwner{}, errors.New("lock owner format is invalid")
	}
	values := make(map[string]string, 4)
	for _, line := range lines[1:5] {
		key, value, ok := strings.Cut(line, "=")
		if !ok || values[key] != "" {
			return lockOwner{}, errors.New("lock owner fields are invalid")
		}
		values[key] = value
	}
	pid, err := strconv.Atoi(values["pid"])
	owner := lockOwner{host: values["host"], pid: pid, target: values["target"], token: values["token"]}
	if err != nil || pid <= 0 || !safeLockValue(owner.host) || !safeLockValue(owner.token) || !filepath.IsAbs(owner.target) || filepath.Clean(owner.target) != owner.target {
		return lockOwner{}, errors.New("lock owner values are invalid")
	}
	return owner, nil
}

func safeLockValue(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && !strings.ContainsRune("._-", character) {
			return false
		}
	}
	return true
}
