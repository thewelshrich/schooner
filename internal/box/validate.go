package box

import (
	"fmt"
	"regexp"
	"strings"
)

var namePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("name must be a lowercase slug of 1-63 letters, numbers, and interior hyphens")
	}
	return nil
}

func ValidateSSHDestination(destination string) error {
	if destination == "" {
		return fmt.Errorf("SSH destination is required")
	}
	if strings.HasPrefix(destination, "-") {
		return fmt.Errorf("SSH destination must not begin with '-'")
	}
	if strings.ContainsAny(destination, "\x00\r\n") {
		return fmt.Errorf("SSH destination contains unsupported control characters")
	}
	return nil
}

func ValidateProjectRoot(root string) error {
	if root == "" {
		return fmt.Errorf("project root is required")
	}
	if strings.ContainsAny(root, "\x00\r\n") {
		return fmt.Errorf("project root contains unsupported control characters")
	}
	if root != "~" && !strings.HasPrefix(root, "~/") && !strings.HasPrefix(root, "/") {
		return fmt.Errorf("project root must be absolute or begin with ~/")
	}
	return nil
}
