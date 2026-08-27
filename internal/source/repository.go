package source

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"
)

// RepositoryIdentity is the transport-independent identity of a network
// repository. Owner may contain path separators for providers that support
// nested namespaces; GitHub identities always contain one owner segment.
type RepositoryIdentity struct {
	Host       string
	Owner      string
	Repository string
}

func (identity RepositoryIdentity) Key() string {
	return identity.Host + "/" + identity.Owner + "/" + identity.Repository
}

func (identity RepositoryIdentity) IsGitHub() bool { return identity.Host == GitHubHost }

func (identity RepositoryIdentity) CanonicalSSH() string {
	if !identity.IsGitHub() {
		return ""
	}
	return "git@" + GitHubHost + ":" + identity.Owner + "/" + identity.Repository + ".git"
}

func (identity RepositoryIdentity) CanonicalHTTPS() string {
	if !identity.IsGitHub() {
		return ""
	}
	return "https://" + GitHubHost + "/" + identity.Owner + "/" + identity.Repository + ".git"
}

// RepositoryIdentityFor normalizes common HTTPS, SSH, Git, and SCP-style
// network transports. Local paths intentionally return network=false.
func RepositoryIdentityFor(raw string) (identity RepositoryIdentity, network bool, err error) {
	if raw == "" || len(raw) > 4096 || containsControl(raw) {
		return RepositoryIdentity{}, false, NewError("invalid_input", "repository source is invalid", nil)
	}
	if !strings.Contains(raw, "://") {
		separator := scpSeparator(raw)
		if separator < 1 || strings.ContainsRune(raw[:separator], '/') {
			return RepositoryIdentity{}, false, nil
		}
		if strings.ContainsAny(raw, "?#") {
			return RepositoryIdentity{}, true, NewError("invalid_input", "repository source must not contain query parameters or fragments", nil)
		}
		authority, repositoryPath := raw[:separator], raw[separator+1:]
		username, host := splitAuthority(authority)
		if !validTransportUsername(username) {
			return RepositoryIdentity{}, true, NewError("invalid_input", "repository SSH source uses an invalid account", nil)
		}
		host = normalizeSCPHost(host)
		if host == GitHubHost && username != "" && username != "git" {
			return RepositoryIdentity{}, true, NewError("invalid_input", "GitHub SSH transport must use the git account", nil)
		}
		return normalizedRepositoryIdentity(host, "", repositoryPath)
	}

	parsed, parseErr := url.Parse(raw)
	if parseErr != nil || parsed.Scheme == "" || parsed.Opaque != "" {
		return RepositoryIdentity{}, true, NewError("invalid_input", "repository source URL is malformed", parseErr)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" && scheme != "ssh" && scheme != "git" {
		return RepositoryIdentity{}, false, nil
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return RepositoryIdentity{}, true, NewError("invalid_input", "repository source must not contain query parameters or fragments", nil)
	}
	username := ""
	if parsed.User != nil {
		username = parsed.User.Username()
		if _, present := parsed.User.Password(); present || (scheme != "ssh" && username != "") {
			return RepositoryIdentity{}, true, NewError("invalid_input", "repository source must not contain embedded credentials", nil)
		}
	}
	if scheme == "ssh" && !validTransportUsername(username) {
		return RepositoryIdentity{}, true, NewError("invalid_input", "repository SSH source uses an invalid account", nil)
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if host == GitHubHost && username != "" && username != "git" {
		return RepositoryIdentity{}, true, NewError("invalid_input", "GitHub SSH transport must use the git account", nil)
	}
	if host == GitHubHost && (scheme != "https" && scheme != "ssh" || port != "" && !defaultRepositoryPort(scheme, port)) {
		return RepositoryIdentity{}, true, NewError("invalid_input", "GitHub repository transport must use HTTPS or SSH on the standard port", nil)
	}
	explicitPort := ""
	if port != "" && !defaultRepositoryPort(scheme, port) {
		host = net.JoinHostPort(host, port)
		explicitPort = port
	}
	repositoryPath := parsed.Path
	return normalizedRepositoryIdentity(host, explicitPort, repositoryPath)
}

func normalizedRepositoryIdentity(host, explicitPort, repositoryPath string) (RepositoryIdentity, bool, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if strings.ContainsRune(repositoryPath, '\\') {
		return RepositoryIdentity{}, true, NewError("invalid_input", "repository source has an invalid identity", nil)
	}
	repositoryPath = strings.Trim(repositoryPath, "/")
	repositoryPath = strings.TrimSuffix(repositoryPath, ".git")
	cleaned := path.Clean(repositoryPath)
	if host == "" || repositoryPath == "" || cleaned != repositoryPath || cleaned == "." || strings.HasPrefix(cleaned, "../") || containsControl(cleaned) {
		return RepositoryIdentity{}, true, NewError("invalid_input", "repository source has no valid network identity", nil)
	}
	parts := strings.Split(cleaned, "/")
	if len(parts) < 2 {
		return RepositoryIdentity{}, true, NewError("invalid_input", "repository source must identify an owner and repository", nil)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, " @:\\") {
			return RepositoryIdentity{}, true, NewError("invalid_input", "repository source has an invalid identity", nil)
		}
	}
	if host == GitHubHost {
		if explicitPort != "" || len(parts) != 2 {
			return RepositoryIdentity{}, true, NewError("invalid_input", "GitHub repository source is invalid", nil)
		}
		return RepositoryIdentity{Host: host, Owner: strings.ToLower(parts[0]), Repository: strings.ToLower(parts[1])}, true, nil
	}
	return RepositoryIdentity{Host: host, Owner: strings.Join(parts[:len(parts)-1], "/"), Repository: parts[len(parts)-1]}, true, nil
}

func splitAuthority(authority string) (username, host string) {
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		return authority[:at], authority[at+1:]
	}
	return "", authority
}

func validTransportUsername(username string) bool {
	return len(username) <= 128 && !containsControl(username) && !strings.ContainsAny(username, " /:@\\")
}

func scpSeparator(value string) int {
	hostStart := 0
	if at := strings.IndexByte(value, '@'); at >= 0 {
		hostStart = at + 1
	}
	if hostStart < len(value) && value[hostStart] == '[' {
		end := strings.IndexByte(value[hostStart+1:], ']')
		if end < 0 {
			return -1
		}
		end += hostStart + 1
		if end+1 >= len(value) || value[end+1] != ':' {
			return -1
		}
		return end + 1
	}
	separator := strings.IndexByte(value[hostStart:], ':')
	if separator < 0 {
		return -1
	}
	return hostStart + separator
}

func normalizeSCPHost(host string) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return strings.ToLower(host[1 : len(host)-1])
	}
	if strings.ContainsRune(host, ':') {
		return ""
	}
	return strings.ToLower(host)
}

func defaultRepositoryPort(scheme, port string) bool {
	switch scheme {
	case "https":
		return port == "443"
	case "http":
		return port == "80"
	case "ssh":
		return port == "22"
	case "git":
		return port == "9418"
	default:
		return false
	}
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

// CloneExecution contains only credential-free operation inputs. Destination
// is owned and validated by the repository lifecycle.
type CloneExecution struct {
	Repository     RepositoryIdentity
	SuppliedOrigin string
	Branch         string
	WorktreeRoot   string
	Destination    string
}

// PrepareCloneAttempt lets the repository lifecycle safely reset only its own
// staging area before a source transport is attempted.
type PrepareCloneAttempt func() error

// CloneExecutor owns transport selection without owning repository staging.
type CloneExecutor interface {
	Clone(context.Context, CloneExecution, PrepareCloneAttempt) error
}

func ValidateCloneExecution(request CloneExecution) error {
	if request.Repository.Host == "" || request.Repository.Owner == "" || request.Repository.Repository == "" || request.SuppliedOrigin == "" || request.WorktreeRoot == "" || request.Destination == "" {
		return fmt.Errorf("clone execution is incomplete")
	}
	return nil
}
