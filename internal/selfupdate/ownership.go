package selfupdate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thewelshrich/schooner/internal/semver"
)

type ownership struct {
	method string
	reason string
}

type installationReceipt struct {
	SchemaVersion      string `json:"schema_version"`
	InstallationMethod string `json:"installation_method"`
	ExecutablePath     string `json:"executable_path"`
	Version            string `json:"version"`
	ExecutableSHA256   string `json:"executable_sha256"`
	ReleaseAssetKind   string `json:"release_asset_kind"`
	ReleaseAssetName   string `json:"release_asset_name"`
	ReleaseAssetSHA256 string `json:"release_asset_sha256"`
	InstalledAt        string `json:"installed_at"`
}

func classifyInstallation(version, executablePath string, invokedSymlink bool) ownership {
	if isHomebrewInstallation(executablePath) {
		return ownership{method: MethodHomebrew}
	}
	if version == "" || version == "dev" {
		return ownership{method: MethodSource}
	}
	if invokedSymlink {
		return ownership{method: MethodUnknown, reason: "Schooner was invoked through a non-Homebrew symlink"}
	}
	if info, err := os.Lstat(executablePath); err != nil {
		return ownership{method: MethodUnknown, reason: "running executable could not be inspected"}
	} else if info.Mode()&os.ModeSymlink != 0 {
		return ownership{method: MethodUnknown, reason: "running executable is a symlink"}
	} else if !info.Mode().IsRegular() {
		return ownership{method: MethodUnknown, reason: "running executable is not a regular file"}
	}
	receiptPath := filepath.Join(filepath.Dir(executablePath), receiptFileName)
	receipt, err := readReceipt(receiptPath, executablePath)
	if err != nil {
		return ownership{method: MethodUnknown, reason: "direct-install receipt is missing, unsafe, malformed, or stale"}
	}
	digest, err := hashFile(executablePath)
	if err != nil || digest != receipt.ExecutableSHA256 || receipt.Version != version {
		return ownership{method: MethodUnknown, reason: "direct-install receipt does not match the running executable"}
	}
	return ownership{method: MethodDirect}
}

func isHomebrewInstallation(executablePath string) bool {
	clean := filepath.Clean(executablePath)
	probe := filepath.Dir(clean)
	for index := 0; index < 6; index++ {
		if strings.EqualFold(filepath.Base(filepath.Dir(probe)), "Cellar") && filepath.Base(probe) == "schooner" {
			return true
		}
		if filepath.Base(probe) == "schooner" {
			if info, err := os.Lstat(filepath.Join(probe, "INSTALL_RECEIPT.json")); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
				return true
			}
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	return strings.Contains(clean, string(filepath.Separator)+"Cellar"+string(filepath.Separator)+"schooner"+string(filepath.Separator))
}

func readReceipt(path, target string) (installationReceipt, error) {
	file, info, err := openRegularNoFollow(path)
	if err != nil {
		return installationReceipt{}, err
	}
	defer file.Close()
	if info.Mode().Perm()&0o022 != 0 {
		return installationReceipt{}, errors.New("receipt is group- or world-writable")
	}
	if err = ownedByCurrentUser(info); err != nil {
		return installationReceipt{}, err
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, 16<<10+1))
	if readErr != nil {
		return installationReceipt{}, readErr
	}
	if len(contents) > 16<<10 {
		return installationReceipt{}, errors.New("receipt exceeds 16 KiB")
	}
	var receipt installationReceipt
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&receipt); err != nil {
		return installationReceipt{}, err
	}
	if err = requireJSONEOF(decoder); err != nil {
		return installationReceipt{}, err
	}
	canonical, err := marshalReceipt(receipt)
	if err != nil || !bytes.Equal(contents, canonical) {
		return installationReceipt{}, errors.New("receipt is not canonical version-1 JSON")
	}
	if err = validateReceipt(receipt, target); err != nil {
		return installationReceipt{}, err
	}
	return receipt, nil
}

func validateReceipt(receipt installationReceipt, target string) error {
	if receipt.SchemaVersion != SchemaVersion || receipt.InstallationMethod != MethodDirect {
		return errors.New("unsupported receipt schema or installation method")
	}
	if receipt.ExecutablePath != target || !filepath.IsAbs(receipt.ExecutablePath) || filepath.Clean(receipt.ExecutablePath) != receipt.ExecutablePath {
		return errors.New("receipt executable path does not match the running executable")
	}
	if !semver.Valid(receipt.Version) || !validDigest(receipt.ExecutableSHA256) || !validDigest(receipt.ReleaseAssetSHA256) {
		return errors.New("receipt version or digest is invalid")
	}
	if receipt.ReleaseAssetKind != "archive" && receipt.ReleaseAssetKind != "raw" {
		return errors.New("receipt release asset kind is invalid")
	}
	if !validAssetName(receipt.ReleaseAssetName) {
		return errors.New("receipt release asset name is invalid")
	}
	installedAt, err := time.Parse(time.RFC3339, receipt.InstalledAt)
	if err != nil || installedAt.UTC().Format(time.RFC3339) != receipt.InstalledAt {
		return errors.New("receipt installation time is invalid")
	}
	return nil
}

func writeReceipt(path string, receipt installationReceipt) error {
	contents, err := marshalReceipt(receipt)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".schooner-receipt.tmp-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(contents)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func marshalReceipt(receipt installationReceipt) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(receipt); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func validAssetName(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && !strings.ContainsRune("._+-", character) {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func hashFile(path string) (string, error) {
	file, _, err := openRegularNoFollow(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fileFingerprint(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "symlink", nil
	}
	if !info.Mode().IsRegular() {
		return "other", nil
	}
	digest, err := hashFile(path)
	if err != nil {
		return "", err
	}
	return "regular:" + digest, nil
}

func revalidateFingerprint(path, baseline string) error {
	current, err := fileFingerprint(path)
	if err != nil {
		return &Error{Code: CodeVerification, Message: fmt.Sprintf("revalidate %s before promotion", filepath.Base(path)), Cause: err}
	}
	if current != baseline {
		return &Error{Code: CodeVerification, Message: fmt.Sprintf("%s changed during update verification; retry", filepath.Base(path))}
	}
	return nil
}
