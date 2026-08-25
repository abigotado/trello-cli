//go:build darwin && cgo && keychainintegration

package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/abigotado/trello-cli/internal/auth"
)

var helperIdentity string

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	if helperIdentity != "A" && helperIdentity != "B" {
		return errors.New("integration helper identity must be A or B")
	}
	if len(arguments) != 2 {
		return errors.New("usage: keychain-helper create|verify|delete ABSOLUTE_PATH")
	}
	switch arguments[0] {
	case "create":
		if helperIdentity != "A" {
			return errors.New("only helper A may create the integration keychain")
		}
		return auth.CreateIntegrationKeychain(ctx, arguments[1])
	case "verify":
		if helperIdentity != "B" {
			return errors.New("only helper B may verify the integration keychain")
		}
		return auth.VerifyIntegrationKeychain(ctx, arguments[1])
	case "delete":
		return auth.DeleteIntegrationKeychain(ctx, arguments[1])
	default:
		return errors.New("usage: keychain-helper create|verify|delete ABSOLUTE_PATH")
	}
}
