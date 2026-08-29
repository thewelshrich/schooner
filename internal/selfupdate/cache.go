package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thewelshrich/schooner/internal/semver"
)

type checkCache struct {
	SchemaVersion    string `json:"schema_version"`
	LastAttemptAt    string `json:"last_attempt_at"`
	LastSuccessfulAt string `json:"last_successful_at,omitempty"`
	LatestVersion    string `json:"latest_version,omitempty"`
	ETag             string `json:"etag,omitempty"`
	LastModified     string `json:"last_modified,omitempty"`
}

type releaseDocument struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func (u *updater) latest(ctx context.Context, mode Mode) (string, bool, error) {
	cache, _ := readCheckCache(u.cachePath)
	now := u.now().UTC()
	if mode == ModeAutomatic {
		if automaticCheckRecent(cache, now) {
			return "", false, nil
		}
		release, claimed := claimAutomaticCheck(u.cachePath, now)
		if !claimed {
			return "", false, nil
		}
		defer release()
		cache, _ = readCheckCache(u.cachePath)
		if automaticCheckRecent(cache, now) {
			return "", false, nil
		}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.apiURL, nil)
	if err != nil {
		return "", false, u.releaseFailure(mode, now, cache, CodeReleaseUnavailable, "create the latest-release request", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("User-Agent", "schooner/"+u.current.Version)
	if cache.ETag != "" {
		request.Header.Set("If-None-Match", cache.ETag)
	}
	if cache.LastModified != "" {
		request.Header.Set("If-Modified-Since", cache.LastModified)
	}
	response, err := u.client.Do(request)
	if err != nil {
		return "", false, u.releaseFailure(mode, now, cache, CodeReleaseUnavailable, "check the latest Schooner release", err)
	}
	defer response.Body.Close()

	cache.SchemaVersion = SchemaVersion
	cache.LastAttemptAt = now.Truncate(time.Second).Format(time.RFC3339)
	switch response.StatusCode {
	case http.StatusNotModified:
		if !semver.Stable(cache.LatestVersion) {
			return "", false, u.releaseFailure(mode, now, cache, CodeInvalidRelease, "GitHub returned an unusable not-modified response", nil)
		}
		cache.LastSuccessfulAt = cache.LastAttemptAt
		_ = writeCheckCache(u.cachePath, cache)
		return cache.LatestVersion, true, nil
	case http.StatusOK:
		contents, readErr := io.ReadAll(io.LimitReader(response.Body, maxReleaseResponse+1))
		if readErr != nil {
			return "", false, u.releaseFailure(mode, now, cache, CodeInvalidRelease, "read the latest-release response", readErr)
		}
		if len(contents) > maxReleaseResponse {
			return "", false, u.releaseFailure(mode, now, cache, CodeInvalidRelease, "latest-release response exceeds 1 MiB", nil)
		}
		var document releaseDocument
		decoder := json.NewDecoder(bytes.NewReader(contents))
		if decodeErr := decoder.Decode(&document); decodeErr != nil {
			return "", false, u.releaseFailure(mode, now, cache, CodeInvalidRelease, "decode the latest-release response", decodeErr)
		}
		if trailingErr := requireJSONEOF(decoder); trailingErr != nil {
			return "", false, u.releaseFailure(mode, now, cache, CodeInvalidRelease, "decode the latest-release response", trailingErr)
		}
		if document.Draft || document.Prerelease || !semver.Stable(document.TagName) {
			return "", false, u.releaseFailure(mode, now, cache, CodeInvalidRelease, "latest release is draft, prerelease, or has an invalid version", nil)
		}
		cache.LatestVersion = document.TagName
		cache.LastSuccessfulAt = cache.LastAttemptAt
		cache.ETag = boundedHeader(response.Header.Get("ETag"))
		cache.LastModified = boundedHeader(response.Header.Get("Last-Modified"))
		_ = writeCheckCache(u.cachePath, cache)
		return document.TagName, true, nil
	default:
		return "", false, u.releaseFailure(mode, now, cache, CodeReleaseUnavailable, fmt.Sprintf("latest release is unavailable (HTTP %d)", response.StatusCode), nil)
	}
}

func automaticCheckRecent(cache checkCache, now time.Time) bool {
	attemptedAt, err := time.Parse(time.RFC3339, cache.LastAttemptAt)
	if err != nil || now.Sub(attemptedAt) < 0 || now.Sub(attemptedAt) >= automaticCheckAge {
		return false
	}
	return true
}

func claimAutomaticCheck(cachePath string, now time.Time) (func(), bool) {
	if cachePath == "" {
		return func() {}, false
	}
	directory := filepath.Dir(cachePath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return func() {}, false
	}
	lockPath := cachePath + ".lock"
	token, err := randomToken()
	if err != nil {
		return func() {}, false
	}
	claim := func() error {
		file, createErr := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			return createErr
		}
		if _, createErr = file.WriteString(token + "\n"); createErr == nil {
			createErr = file.Sync()
		}
		if closeErr := file.Close(); createErr == nil {
			createErr = closeErr
		}
		if createErr != nil {
			_ = os.Remove(lockPath)
		}
		return createErr
	}
	if err = claim(); errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(lockPath)
		if statErr != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) <= 5*time.Minute {
			return func() {}, false
		}
		stalePath := lockPath + ".stale." + token
		if os.Rename(lockPath, stalePath) != nil {
			return func() {}, false
		}
		_ = os.Remove(stalePath)
		err = claim()
	}
	if err != nil {
		return func() {}, false
	}
	return func() {
		contents, readErr := os.ReadFile(lockPath)
		if readErr == nil && string(contents) == token+"\n" {
			_ = os.Remove(lockPath)
		}
	}, true
}

func (u *updater) releaseFailure(mode Mode, now time.Time, cache checkCache, code Code, message string, cause error) error {
	cache.SchemaVersion = SchemaVersion
	cache.LastAttemptAt = now.UTC().Truncate(time.Second).Format(time.RFC3339)
	_ = writeCheckCache(u.cachePath, cache)
	if mode == ModeAutomatic {
		return nil
	}
	return &Error{Code: code, Message: message, Cause: cause}
}

func readCheckCache(path string) (checkCache, error) {
	if path == "" {
		return checkCache{}, errors.New("update-check cache path is unavailable")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return checkCache{}, err
	}
	if len(contents) > 64<<10 {
		return checkCache{}, errors.New("update-check cache exceeds 64 KiB")
	}
	var cache checkCache
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cache); err != nil {
		return checkCache{}, err
	}
	if err = requireJSONEOF(decoder); err != nil {
		return checkCache{}, err
	}
	if cache.SchemaVersion != SchemaVersion {
		return checkCache{}, fmt.Errorf("unsupported update-check cache schema %q", cache.SchemaVersion)
	}
	return cache, nil
}

func writeCheckCache(path string, cache checkCache) error {
	if path == "" {
		return errors.New("update-check cache path is unavailable")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".update-check-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		encoder := json.NewEncoder(temporary)
		encoder.SetEscapeHTML(false)
		err = encoder.Encode(cache)
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

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("JSON contains trailing values")
	}
	return err
}

func boundedHeader(value string) string {
	if len(value) > 1024 || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	return value
}
