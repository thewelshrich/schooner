package cli

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/thewelshrich/schooner/internal/source"
)

type devicePresenter struct {
	streams Streams
	goos    string
	run     func(context.Context, string, ...string) error
}

func newDevicePresenter(streams Streams) source.DevicePresenter {
	return &devicePresenter{streams: streams, goos: runtime.GOOS, run: func(ctx context.Context, name string, args ...string) error {
		return exec.CommandContext(ctx, name, args...).Run()
	}}
}

func (p *devicePresenter) Present(ctx context.Context, authorization source.DeviceAuthorization) error {
	parsed, err := url.Parse(authorization.VerificationURI)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || authorization.UserCode == "" || len(authorization.UserCode) > 32 || strings.ContainsAny(authorization.UserCode, "\x00\r\n") {
		return fmt.Errorf("GitHub returned an invalid device authorization URL")
	}
	_, _ = fmt.Fprintf(p.streams.Err, "Open %s and enter code %s\n", authorization.VerificationURI, authorization.UserCode)
	command := ""
	switch p.goos {
	case "darwin":
		command = "open"
	case "linux":
		command = "xdg-open"
	}
	if command != "" {
		if err = p.run(ctx, command, authorization.VerificationURI); err != nil {
			_, _ = fmt.Fprintln(p.streams.Err, "Warning: Could not open a browser automatically; use the URL and code above.")
		}
	}
	return nil
}
