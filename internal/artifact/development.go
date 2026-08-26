package artifact

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const developmentLockName = ".generation.lock"

type DevelopmentBuildOptions struct {
	SourceDir string
	OutputDir string
	GoBinary  string
}

type DevelopmentBuild struct {
	Directory string
	Artifacts []Result
}

func BuildDevelopment(ctx context.Context, options DevelopmentBuildOptions) (DevelopmentBuild, error) {
	sourceDir := defaultString(options.SourceDir, ".")
	goBinary := defaultString(options.GoBinary, "go")
	outputDir := options.OutputDir
	if outputDir == "" {
		var err error
		outputDir, err = effectiveDevelopmentDirectory()
		if err != nil {
			return DevelopmentBuild{}, err
		}
	}

	var err error
	if sourceDir, err = filepath.Abs(sourceDir); err != nil {
		return DevelopmentBuild{}, fmt.Errorf("resolve development source directory: %w", err)
	}
	if outputDir, err = filepath.Abs(outputDir); err != nil {
		return DevelopmentBuild{}, fmt.Errorf("resolve development artifact directory: %w", err)
	}
	if err = os.MkdirAll(outputDir, 0o700); err != nil {
		return DevelopmentBuild{}, fmt.Errorf("create development artifact directory: %w", err)
	}
	buildLock, err := acquireDevelopmentBuildLock(outputDir)
	if err != nil {
		return DevelopmentBuild{}, fmt.Errorf("lock development artifact directory: %w", err)
	}
	defer buildLock.Release()

	activeDirectory, publishedGeneration, err := activeDevelopmentDirectory(outputDir)
	if err != nil {
		return DevelopmentBuild{}, err
	}
	activeGeneration := ""
	if publishedGeneration {
		activeGeneration = filepath.Base(activeDirectory)
	}
	if err = cleanupDevelopmentGenerations(outputDir, activeGeneration); err != nil {
		return DevelopmentBuild{}, fmt.Errorf("clean development artifact generations: %w", err)
	}

	generationDir, err := os.MkdirTemp(outputDir, developmentGenerationPrefix)
	if err != nil {
		return DevelopmentBuild{}, fmt.Errorf("create development build directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(generationDir)
		}
	}()

	build := DevelopmentBuild{Directory: outputDir}
	var manifest strings.Builder
	for _, arch := range []string{"amd64", "arm64"} {
		platform := Platform{OS: "linux", Arch: arch}
		name := fileName("dev", platform)
		path := filepath.Join(generationDir, name)
		command := exec.CommandContext(ctx, goBinary, "build", "-trimpath", "-o", path, "./cmd/schooner")
		command.Dir = sourceDir
		command.Env = developmentEnvironment(os.Environ(), arch)
		output, buildErr := command.CombinedOutput()
		if buildErr != nil {
			if ctx.Err() != nil {
				return DevelopmentBuild{}, ctx.Err()
			}
			return DevelopmentBuild{}, fmt.Errorf("build linux/%s host runtime: %w: %s", arch, buildErr, strings.TrimSpace(string(output)))
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return DevelopmentBuild{}, fmt.Errorf("read linux/%s host runtime: %w", arch, readErr)
		}
		if len(contents) == 0 {
			return DevelopmentBuild{}, fmt.Errorf("build linux/%s host runtime: output is empty", arch)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(contents))
		fmt.Fprintf(&manifest, "%s  %s\n", digest, name)
		build.Artifacts = append(build.Artifacts, Result{Path: path, Version: "dev", Platform: platform, SHA256: digest})
	}
	if err = os.WriteFile(filepath.Join(generationDir, manifestName), []byte(manifest.String()), 0o600); err != nil {
		return DevelopmentBuild{}, fmt.Errorf("write development checksum manifest: %w", err)
	}
	for _, result := range build.Artifacts {
		if _, err = readManifest(filepath.Join(generationDir, manifestName), filepath.Base(result.Path)); err != nil {
			return DevelopmentBuild{}, err
		}
		if err = verifyFile(result.Path, result.SHA256); err != nil {
			return DevelopmentBuild{}, err
		}
	}
	if err = publishDevelopmentGeneration(outputDir, generationDir); err != nil {
		return DevelopmentBuild{}, err
	}
	published = true
	_ = cleanupDevelopmentGenerations(outputDir, filepath.Base(generationDir))
	return build, nil
}

func publishDevelopmentGeneration(outputDir, generationDir string) error {
	pointer, err := os.CreateTemp(outputDir, ".current-")
	if err != nil {
		return fmt.Errorf("create development artifact pointer: %w", err)
	}
	pointerPath := pointer.Name()
	defer os.Remove(pointerPath)

	if _, err = fmt.Fprintln(pointer, filepath.Base(generationDir)); err != nil {
		_ = pointer.Close()
		return fmt.Errorf("write development artifact pointer: %w", err)
	}
	if err = pointer.Sync(); err != nil {
		_ = pointer.Close()
		return fmt.Errorf("sync development artifact pointer: %w", err)
	}
	if err = pointer.Close(); err != nil {
		return fmt.Errorf("close development artifact pointer: %w", err)
	}
	if err = os.Rename(pointerPath, filepath.Join(outputDir, developmentCurrentName)); err != nil {
		return fmt.Errorf("publish development artifacts: %w", err)
	}
	return nil
}

type developmentLease struct {
	file *os.File
	once sync.Once
	err  error
}

func (l *developmentLease) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		closeErr := l.file.Close()
		l.err = errors.Join(unlockErr, closeErr)
	})
	return l.err
}

func acquireDevelopmentBuildLock(outputDir string) (*developmentLease, error) {
	return acquireDevelopmentLease(filepath.Join(outputDir, developmentLockName), syscall.LOCK_EX)
}

func acquireDevelopmentGenerationLease(generationDir string, nonBlocking bool) (*developmentLease, error) {
	operation := syscall.LOCK_SH
	if nonBlocking {
		operation = syscall.LOCK_EX | syscall.LOCK_NB
	}
	return acquireDevelopmentLease(filepath.Join(generationDir, developmentLockName), operation)
}

func acquireDevelopmentLease(path string, operation int) (*developmentLease, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), operation); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &developmentLease{file: file}, nil
}

func cleanupDevelopmentGenerations(outputDir, activeGeneration string) error {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, developmentGenerationPrefix) || name == activeGeneration {
			continue
		}
		generationDir := filepath.Join(outputDir, name)
		lease, lockErr := acquireDevelopmentGenerationLease(generationDir, true)
		if errors.Is(lockErr, syscall.EWOULDBLOCK) || errors.Is(lockErr, syscall.EAGAIN) {
			continue
		}
		if errors.Is(lockErr, os.ErrNotExist) {
			continue
		}
		if lockErr != nil {
			return lockErr
		}
		removeErr := os.RemoveAll(generationDir)
		releaseErr := lease.Release()
		if removeErr != nil || releaseErr != nil {
			return errors.Join(removeErr, releaseErr)
		}
	}
	return nil
}

func developmentEnvironment(environment []string, arch string) []string {
	values := map[string]string{"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": arch}
	result := make([]string, 0, len(environment)+len(values))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			result = append(result, entry)
		}
	}
	for _, key := range []string{"CGO_ENABLED", "GOOS", "GOARCH"} {
		result = append(result, key+"="+values[key])
	}
	return result
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
