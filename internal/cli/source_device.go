package cli

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/thewelshrich/schooner/internal/source"
	"github.com/thewelshrich/schooner/internal/ui/prompts"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

type devicePresenter struct {
	streams    Streams
	theme      *uitheme.Theme
	accessible bool
	goos       string
	run        func(context.Context, string, ...string) error
}

func newDevicePresenter(streams Streams, theme *uitheme.Theme, accessible bool) source.DevicePresenter {
	return &devicePresenter{streams: streams, theme: theme, accessible: accessible, goos: runtime.GOOS, run: func(ctx context.Context, name string, args ...string) error {
		return exec.CommandContext(ctx, name, args...).Run()
	}}
}

func (p *devicePresenter) Present(ctx context.Context, authorization source.DeviceAuthorization) error {
	parsed, err := url.Parse(authorization.VerificationURI)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || authorization.UserCode == "" || len(authorization.UserCode) > 32 || strings.ContainsAny(authorization.UserCode, "\x00\r\n") {
		return fmt.Errorf("GitHub returned an invalid device authorization URL")
	}
	if err = writeFeaturedSummary(p.streams.Err, p.theme, "Authorize Schooner", []summaryRow{
		{Label: "Open", Value: authorization.VerificationURI},
		{Label: "Code", Value: authorization.UserCode, Emphasize: true},
	}); err != nil {
		return err
	}
	if err = writeExplanation(p.streams.Err, p.theme, "In the browser, authorize Git SSH key access. That lets Schooner register this Box's public key. It does not grant repository access."); err != nil {
		return err
	}
	command := ""
	switch p.goos {
	case "darwin":
		command = "open"
	case "linux":
		command = "xdg-open"
	}
	if command != "" {
		if err = p.run(ctx, command, authorization.VerificationURI); err != nil {
			if warnErr := writeWarningLine(p.streams.Err, p.theme, "Could not open a browser automatically; use the URL and code above."); warnErr != nil {
				return warnErr
			}
		}
	}
	return nil
}

func (p *devicePresenter) Wait(ctx context.Context, title string, action func(context.Context) error) error {
	return prompts.Wait(ctx, prompts.Options{Output: p.streams.Err, Theme: p.theme, Accessible: p.accessible}, title, action)
}
