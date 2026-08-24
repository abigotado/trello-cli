//go:build !darwin

package auth

import (
	"context"
	"errors"

	"github.com/zalando/go-keyring"
)

type goKeyringBackend struct{}

func platformKeyringBackend() keyringBackend {
	return goKeyringBackend{}
}

func (goKeyringBackend) get(ctx context.Context, service, account string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := keyring.Get(service, account)
	return value, translateGoKeyringError(err)
}

func (goKeyringBackend) set(ctx context.Context, service, account, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return translateGoKeyringError(keyring.Set(service, account, value))
}

func (backend goKeyringBackend) setForLogin(ctx context.Context, service, primaryAccount, _ string, value string) error {
	return backend.set(ctx, service, primaryAccount, value)
}

func (goKeyringBackend) delete(ctx context.Context, service, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return translateGoKeyringError(keyring.Delete(service, account))
}

func translateGoKeyringError(err error) error {
	if errors.Is(err, keyring.ErrNotFound) {
		return errKeyringNotFound
	}
	return err
}
