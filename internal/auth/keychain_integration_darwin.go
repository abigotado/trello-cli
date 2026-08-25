//go:build darwin && cgo && keychainintegration

package auth

/*
#include <stdlib.h>
#include <Security/Security.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"unsafe"
)

const (
	integrationAccount  = "cross-binary"
	integrationPassword = "trello-cli-synthetic-keychain-password"
)

var integrationCredentials = Credentials{
	APIKey: "synthetic-api-key-marker",
	Token:  "synthetic-token-marker",
}

var updatedIntegrationCredentials = Credentials{
	APIKey: "updated-synthetic-api-key-marker",
	Token:  "updated-synthetic-token-marker",
}

var legacyIntegrationCredentials = Credentials{
	APIKey: "legacy-synthetic-api-key-marker",
	Token:  "legacy-synthetic-token-marker",
}

// CreateIntegrationKeychain creates a disposable, isolated keychain and
// writes only synthetic credentials through the production SaveForLogin path.
// Both items start with an allow-any ACL so a separately linked helper can
// exercise cross-binary access and idempotent migration without authentication
// UI. Creator-scoped ACL mutation is intentionally not exercised in headless CI.
func CreateIntegrationKeychain(ctx context.Context, path string) error {
	if err := validateIntegrationKeychainPath(path, true); err != nil {
		return err
	}
	addPath := integrationAddTargetPath(path)
	if err := validateIntegrationKeychainPath(addPath, true); err != nil {
		return err
	}
	searchKeychain, err := createIntegrationKeychain(path)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = C.SecKeychainDelete(searchKeychain)
		}
		C.CFRelease(C.CFTypeRef(searchKeychain))
	}()
	addKeychain, err := createIntegrationKeychain(addPath)
	if err != nil {
		return err
	}
	defer func() {
		if !keep {
			_ = C.SecKeychainDelete(addKeychain)
		}
		C.CFRelease(C.CFTypeRef(addKeychain))
	}()
	if err := unlockIntegrationKeychain(searchKeychain); err != nil {
		return err
	}
	if err := unlockIntegrationKeychain(addKeychain); err != nil {
		return err
	}
	backend := securityFrameworkBackend{
		searchKeychain: searchKeychain,
		addKeychain:    searchKeychain,
	}
	store := KeyringStore{backend: backend}
	if err := store.SaveForLogin(ctx, integrationAccount, integrationCredentials); err != nil {
		return err
	}
	legacyBlob, err := json.Marshal(legacyIntegrationCredentials)
	if err != nil {
		return err
	}
	// KeyringStore.SaveForLogin maps DefaultAccount to account:default. Call
	// the same production backend path directly to retain the pre-accounts
	// physical account name in this compatibility fixture.
	if err := backend.setForLogin(ctx, KeyringService, legacyUser, "", string(legacyBlob)); err != nil {
		return err
	}
	status := C.SecKeychainLock(addKeychain)
	if status != C.errSecSuccess {
		return translateKeychainStatus("lock integration add keychain", int64(status))
	}
	status = C.SecKeychainLock(searchKeychain)
	if status != C.errSecSuccess {
		return translateKeychainStatus("lock integration search keychain", int64(status))
	}
	keep = true
	return nil
}

// VerifyIntegrationKeychain explicitly unlocks a disposable keychain and
// verifies cross-binary access and idempotent migration without rendering
// credential material.
func VerifyIntegrationKeychain(ctx context.Context, path string) error {
	searchKeychain, releaseSearch, err := openIntegrationKeychain(path)
	if err != nil {
		return err
	}
	defer releaseSearch()
	addKeychain, releaseAdd, err := openIntegrationKeychain(integrationAddTargetPath(path))
	if err != nil {
		return err
	}
	defer releaseAdd()
	if err := unlockIntegrationKeychain(searchKeychain); err != nil {
		return err
	}
	if err := unlockIntegrationKeychain(addKeychain); err != nil {
		return err
	}
	store := KeyringStore{backend: securityFrameworkBackend{
		searchKeychain: searchKeychain,
		addKeychain:    addKeychain,
	}}
	searchStore := KeyringStore{backend: securityFrameworkBackend{
		searchKeychain: searchKeychain,
		addKeychain:    searchKeychain,
	}}
	addStore := KeyringStore{backend: securityFrameworkBackend{
		searchKeychain: addKeychain,
		addKeychain:    addKeychain,
	}}
	credentials, err := store.Load(ctx, integrationAccount)
	if err != nil {
		return err
	}
	if err := validateIntegrationCredentials(credentials); err != nil {
		return err
	}
	credentials, err = store.Load(ctx, DefaultAccount)
	if err != nil {
		return err
	}
	if err := validateLegacyIntegrationCredentials(credentials); err != nil {
		return err
	}
	for range 2 {
		if err := store.MigrateKeychain(ctx, integrationAccount); err != nil {
			return err
		}
		credentials, err = store.Load(ctx, integrationAccount)
		if err != nil {
			return err
		}
		if err := validateIntegrationCredentials(credentials); err != nil {
			return err
		}
		if err := store.MigrateKeychain(ctx, DefaultAccount); err != nil {
			return err
		}
		credentials, err = store.Load(ctx, DefaultAccount)
		if err != nil {
			return err
		}
		if err := validateLegacyIntegrationCredentials(credentials); err != nil {
			return err
		}
	}
	restoreNeeded := false
	defer func() {
		if restoreNeeded {
			// Best-effort restoration keeps the synthetic marker available to
			// the safety-checked cleanup path after an intermediate failure.
			_ = searchStore.Save(ctx, integrationAccount, integrationCredentials)
		}
	}()
	if err := store.Save(ctx, integrationAccount, updatedIntegrationCredentials); err != nil {
		return err
	}
	restoreNeeded = true
	credentials, err = store.Load(ctx, integrationAccount)
	if err != nil {
		return err
	}
	if credentials != updatedIntegrationCredentials {
		return errors.New("integration keychain update verification failed")
	}
	credentials, err = addStore.Load(ctx, integrationAccount)
	if err != nil {
		return err
	}
	if credentials != (Credentials{}) {
		return errors.New("integration add keychain unexpectedly contains the synthetic credential")
	}
	if err := searchStore.Delete(ctx, integrationAccount); err != nil {
		return err
	}
	credentials, err = searchStore.Load(ctx, integrationAccount)
	if err != nil {
		return err
	}
	if credentials != (Credentials{}) {
		return errors.New("integration keychain delete verification failed")
	}
	if err := searchStore.Save(ctx, integrationAccount, integrationCredentials); err != nil {
		return err
	}
	restoreNeeded = false
	return nil
}

// DeleteIntegrationKeychain deletes only a disposable keychain containing the
// exact synthetic marker written by CreateIntegrationKeychain.
func DeleteIntegrationKeychain(ctx context.Context, path string) error {
	_, searchErr := os.Stat(path)
	_, addErr := os.Stat(integrationAddTargetPath(path))
	if errors.Is(searchErr, os.ErrNotExist) && errors.Is(addErr, os.ErrNotExist) {
		return nil
	}
	if searchErr != nil {
		return searchErr
	}
	if addErr != nil {
		return addErr
	}
	searchKeychain, releaseSearch, err := openIntegrationKeychain(path)
	if err != nil {
		return err
	}
	defer releaseSearch()
	addKeychain, releaseAdd, err := openIntegrationKeychain(integrationAddTargetPath(path))
	if err != nil {
		return err
	}
	defer releaseAdd()
	if err := unlockIntegrationKeychain(searchKeychain); err != nil {
		return err
	}
	if err := unlockIntegrationKeychain(addKeychain); err != nil {
		return err
	}
	searchStore := KeyringStore{backend: securityFrameworkBackend{
		searchKeychain: searchKeychain,
		addKeychain:    searchKeychain,
	}}
	credentials, err := searchStore.Load(ctx, integrationAccount)
	if err != nil {
		return err
	}
	if err := validateIntegrationCredentials(credentials); err != nil {
		return err
	}
	credentials, err = searchStore.Load(ctx, DefaultAccount)
	if err != nil {
		return err
	}
	if err := validateLegacyIntegrationCredentials(credentials); err != nil {
		return err
	}
	addStore := KeyringStore{backend: securityFrameworkBackend{
		searchKeychain: addKeychain,
		addKeychain:    addKeychain,
	}}
	credentials, err = addStore.Load(ctx, integrationAccount)
	if err != nil {
		return err
	}
	if credentials != (Credentials{}) {
		return errors.New("integration add keychain unexpectedly contains the synthetic credential")
	}
	status := C.SecKeychainDelete(addKeychain)
	if status != C.errSecSuccess {
		return translateKeychainStatus("delete integration add keychain", int64(status))
	}
	status = C.SecKeychainDelete(searchKeychain)
	return translateKeychainStatus("delete integration search keychain", int64(status))
}

func validateIntegrationCredentials(credentials Credentials) error {
	if credentials.APIKey != integrationCredentials.APIKey {
		if credentials == (Credentials{}) {
			return errors.New("integration keychain is missing the synthetic credential")
		}
		if credentials.APIKey == updatedIntegrationCredentials.APIKey {
			return errors.New("integration keychain still contains the updated synthetic API key marker")
		}
		return errors.New("integration keychain contains an unexpected synthetic API key marker")
	}
	if credentials.Token != integrationCredentials.Token {
		return errors.New("integration keychain contains an unexpected synthetic token marker")
	}
	return nil
}

func validateLegacyIntegrationCredentials(credentials Credentials) error {
	if credentials == (Credentials{}) {
		return errors.New("integration keychain is missing the legacy synthetic credential")
	}
	if credentials != legacyIntegrationCredentials {
		return errors.New("integration keychain contains an unexpected legacy synthetic credential")
	}
	return nil
}

func integrationAddTargetPath(path string) string {
	return path + ".add-target"
}

func validateIntegrationKeychainPath(path string, mustNotExist bool) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("integration keychain path must be absolute")
	}
	if !mustNotExist {
		return nil
	}
	_, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	default:
		return errors.New("integration keychain path already exists")
	}
}

func createIntegrationKeychain(path string) (C.SecKeychainRef, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var keychain C.SecKeychainRef
	status := C.SecKeychainCreate(
		cPath,
		C.UInt32(len(integrationPassword)),
		unsafe.Pointer(unsafe.StringData(integrationPassword)),
		C.false,
		nullSecurityRef,
		&keychain,
	)
	if status != C.errSecSuccess {
		releaseCFType(C.CFTypeRef(keychain))
		return nullSecurityRef, translateKeychainStatus("create integration keychain", int64(status))
	}
	if keychain == nullSecurityRef {
		return nullSecurityRef, internalKeychainError("create integration keychain")
	}
	return keychain, nil
}

func openIntegrationKeychain(path string) (C.SecKeychainRef, func(), error) {
	if err := validateIntegrationKeychainPath(path, false); err != nil {
		return nullSecurityRef, func() {}, err
	}
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var keychain C.SecKeychainRef
	status := C.SecKeychainOpen(cPath, &keychain)
	if status != C.errSecSuccess {
		releaseCFType(C.CFTypeRef(keychain))
		return nullSecurityRef, func() {}, translateKeychainStatus("open integration keychain", int64(status))
	}
	if keychain == nullSecurityRef {
		return nullSecurityRef, func() {}, internalKeychainError("open integration keychain")
	}
	return keychain, func() { C.CFRelease(C.CFTypeRef(keychain)) }, nil
}

func unlockIntegrationKeychain(keychain C.SecKeychainRef) error {
	status := C.SecKeychainUnlock(
		keychain,
		C.UInt32(len(integrationPassword)),
		unsafe.Pointer(unsafe.StringData(integrationPassword)),
		C.true,
	)
	return translateKeychainStatus("unlock integration keychain", int64(status))
}
