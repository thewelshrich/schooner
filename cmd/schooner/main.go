package main

import (
	"context"
	"os"
	"os/signal"
	"runtime"

	"github.com/thewelshrich/schooner/internal/cli"
	"golang.org/x/term"
)

var (
	version        = "dev"
	commit         = "unknown"
	builtAt        = ""
	githubClientID = ""
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer stop()
	executablePath, _ := os.Executable()

	code := cli.Run(ctx, os.Args[1:], cli.Streams{
		In:            os.Stdin,
		Out:           os.Stdout,
		Err:           os.Stderr,
		InIsTerminal:  term.IsTerminal(int(os.Stdin.Fd())),
		OutIsTerminal: term.IsTerminal(int(os.Stdout.Fd())),
		ErrIsTerminal: term.IsTerminal(int(os.Stderr.Fd())),
	}, cli.BuildInfo{
		Version:        version,
		Commit:         commit,
		BuiltAt:        builtAt,
		GoVersion:      runtime.Version(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		GitHubClientID: githubClientID,
		ExecutablePath: executablePath,
		InvocationPath: os.Args[0],
	})

	os.Exit(code)
}
