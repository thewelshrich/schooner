//go:build linux

package selfupdate

import "context"

func verifyPlatformSignature(context.Context, string) error { return nil }
