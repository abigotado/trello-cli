//go:build !darwin

package auth

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestTranslateGoKeyringError(t *testing.T) {
	backendFailure := errors.New("backend unavailable")
	tests := []struct {
		name         string
		err          error
		wantNotFound bool
		wantSame     bool
	}{
		{name: "nil remains nil"},
		{name: "go-keyring missing maps to logical missing", err: keyring.ErrNotFound, wantNotFound: true},
		{name: "wrapped go-keyring missing maps to logical missing", err: errors.Join(backendFailure, keyring.ErrNotFound), wantNotFound: true},
		{name: "other backend failure remains unchanged", err: backendFailure, wantSame: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translateGoKeyringError(tt.err)
			switch {
			case tt.wantNotFound:
				if !errors.Is(got, errKeyringNotFound) {
					t.Errorf("error = %v, want errKeyringNotFound", got)
				}
			case tt.wantSame:
				if !errors.Is(got, tt.err) {
					t.Errorf("error = %v, want original backend failure", got)
				}
			case got != nil:
				t.Errorf("error = %v, want nil", got)
			}
		})
	}
}
