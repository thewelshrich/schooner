// Package semver validates and compares v-prefixed semantic versions.
package semver

import "strings"

type version struct {
	major      string
	minor      string
	patch      string
	prerelease []string
}

func Valid(value string) bool {
	_, ok := parse(value)
	return ok
}

func Compare(left, right string) (int, bool) {
	a, ok := parse(left)
	if !ok {
		return 0, false
	}
	b, ok := parse(right)
	if !ok {
		return 0, false
	}
	for _, pair := range [][2]string{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if result := compareNumeric(pair[0], pair[1]); result != 0 {
			return result, true
		}
	}
	if len(a.prerelease) == 0 && len(b.prerelease) == 0 {
		return 0, true
	}
	if len(a.prerelease) == 0 {
		return 1, true
	}
	if len(b.prerelease) == 0 {
		return -1, true
	}
	for index := 0; index < min(len(a.prerelease), len(b.prerelease)); index++ {
		leftIdentifier, rightIdentifier := a.prerelease[index], b.prerelease[index]
		leftNumeric, rightNumeric := numeric(leftIdentifier), numeric(rightIdentifier)
		switch {
		case leftNumeric && rightNumeric:
			if result := compareNumeric(leftIdentifier, rightIdentifier); result != 0 {
				return result, true
			}
		case leftNumeric:
			return -1, true
		case rightNumeric:
			return 1, true
		case leftIdentifier < rightIdentifier:
			return -1, true
		case leftIdentifier > rightIdentifier:
			return 1, true
		}
	}
	switch {
	case len(a.prerelease) < len(b.prerelease):
		return -1, true
	case len(a.prerelease) > len(b.prerelease):
		return 1, true
	default:
		return 0, true
	}
}

func parse(value string) (version, bool) {
	if len(value) < 2 || value[0] != 'v' {
		return version{}, false
	}
	value = value[1:]
	coreAndPre, build, hasBuild := strings.Cut(value, "+")
	if hasBuild && (!validIdentifiers(build, false) || strings.Contains(build, "+")) {
		return version{}, false
	}
	core, prerelease, hasPrerelease := strings.Cut(coreAndPre, "-")
	if hasPrerelease && !validIdentifiers(prerelease, true) {
		return version{}, false
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 || !validNumeric(parts[0]) || !validNumeric(parts[1]) || !validNumeric(parts[2]) {
		return version{}, false
	}
	result := version{major: parts[0], minor: parts[1], patch: parts[2]}
	if hasPrerelease {
		result.prerelease = strings.Split(prerelease, ".")
	}
	return result, true
}

func validIdentifiers(value string, rejectLeadingZero bool) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		isNumeric := true
		for _, character := range identifier {
			if (character < '0' || character > '9') && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && character != '-' {
				return false
			}
			if character < '0' || character > '9' {
				isNumeric = false
			}
		}
		if rejectLeadingZero && isNumeric && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

func validNumeric(value string) bool {
	return value != "" && (len(value) == 1 || value[0] != '0') && numeric(value)
}

func numeric(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func compareNumeric(left, right string) int {
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
