//go:build darwin && !cgo

package auth

import (
	"context"
)

type unsupportedKeyringBackend struct{}

func platformKeyringBackend() keyringBackend {
	return unsupportedKeyringBackend{}
}

func (unsupportedKeyringBackend) get(ctx context.Context, _, _ string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", errKeyringUnsupported
}

func (unsupportedKeyringBackend) set(ctx context.Context, _, _, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errKeyringUnsupported
}

func (backend unsupportedKeyringBackend) setForLogin(ctx context.Context, service, primaryAccount, _ string, value string) error {
	return backend.set(ctx, service, primaryAccount, value)
}

func (unsupportedKeyringBackend) migrate(ctx context.Context, _, _, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errKeyringUnsupported
}

func (unsupportedKeyringBackend) delete(ctx context.Context, _, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errKeyringUnsupported
}
