//go:build darwin && cgo && keychainintegration

package auth

/*
#include <stdlib.h>
#include <Security/Security.h>

static OSStatus trello_integration_allow_any_change_acl(SecAccessRef access) {
	CFArrayRef aclList = SecAccessCopyMatchingACLList(access, kSecACLAuthorizationChangeACL);
	if (aclList == NULL) return errSecInternalComponent;
	if (CFArrayGetCount(aclList) != 1) {
		CFRelease(aclList);
		return errSecInternalComponent;
	}
	SecACLRef acl = (SecACLRef)CFArrayGetValueAtIndex(aclList, 0);
	CFArrayRef applicationList = NULL;
	CFStringRef description = NULL;
	SecKeychainPromptSelector promptSelector = 0;
	OSStatus status = SecACLCopyContents(acl, &applicationList, &description, &promptSelector);
	if (status == errSecSuccess) {
		SecKeychainPromptSelector normalizedPromptSelector = promptSelector & ~kSecKeychainPromptRequirePassphase;
		if (applicationList != NULL || normalizedPromptSelector != promptSelector) {
			status = SecACLSetContents(acl, NULL, description, normalizedPromptSelector);
		}
	}
	if (applicationList != NULL) CFRelease(applicationList);
	if (description != NULL) CFRelease(description);
	CFRelease(aclList);
	return status;
}

static OSStatus trello_integration_add_creator_item(CFStringRef service, CFStringRef account, SecKeychainRef keychain, CFDataRef value) {
	SecTrustedApplicationRef application = NULL;
	OSStatus status = SecTrustedApplicationCreateFromPath(NULL, &application);
	if (status != errSecSuccess) {
		if (application != NULL) CFRelease(application);
		return status;
	}
	const void *trustedValues[] = {application};
	CFArrayRef trustedApplications = CFArrayCreate(kCFAllocatorDefault, trustedValues, 1, &kCFTypeArrayCallBacks);
	CFRelease(application);
	if (trustedApplications == NULL) return errSecAllocate;
	SecAccessRef access = NULL;
	status = SecAccessCreate(service, trustedApplications, &access);
	CFRelease(trustedApplications);
	if (status != errSecSuccess) {
		if (access != NULL) CFRelease(access);
		return status;
	}
	status = trello_integration_allow_any_change_acl(access);
	if (status != errSecSuccess) {
		CFRelease(access);
		return status;
	}
	CFMutableDictionaryRef attributes = CFDictionaryCreateMutable(kCFAllocatorDefault, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (attributes == NULL) {
		CFRelease(access);
		return errSecAllocate;
	}
	CFDictionarySetValue(attributes, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(attributes, kSecAttrService, service);
	CFDictionarySetValue(attributes, kSecAttrAccount, account);
	CFDictionarySetValue(attributes, kSecValueData, value);
	CFDictionarySetValue(attributes, kSecUseKeychain, keychain);
	CFDictionarySetValue(attributes, kSecAttrAccess, access);
	status = SecItemAdd(attributes, NULL);
	CFRelease(attributes);
	CFRelease(access);
	return status;
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/abigotado/trello-cli/internal/errx"
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
// writes only synthetic credentials through the compatible Security.framework
// path used to exercise production migration. Decrypt access remains scoped to
// helper A, while ChangeACL is allow-any so helper B can exercise ACL mutation
// without UI. Real creator-scoped legacy items may prompt during migration.
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
		addKeychain: searchKeychain,
	}
	items := []struct {
		account     string
		credentials Credentials
	}{
		{account: entryName(integrationAccount), credentials: integrationCredentials},
		{account: legacyUser, credentials: legacyIntegrationCredentials},
	}
	for _, item := range items {
		blob, err := json.Marshal(item.credentials)
		if err != nil {
			return err
		}
		if err := addCreatorScopedIntegrationItem(ctx, backend, KeyringService, item.account, string(blob)); err != nil {
			return err
		}
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

func addCreatorScopedIntegrationItem(ctx context.Context, backend securityFrameworkBackend, service, account, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := encodeGoKeyringValue(value)
	if err != nil {
		return err
	}
	refs, release, err := makeKeychainWriteRefs(service, account, encoded)
	if err != nil {
		return err
	}
	defer release()
	status := C.trello_integration_add_creator_item(refs.service, refs.account, backend.addKeychain, refs.value)
	return translateKeychainStatus("write creator-scoped integration item", int64(status))
}

// VerifyIntegrationKeychain explicitly unlocks a disposable keychain and
// verifies its synthetic credential without rendering credential material.
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
	_, err = store.Load(ctx, integrationAccount)
	if err := requireIntegrationMigration(err, "named"); err != nil {
		return err
	}
	_, err = store.Load(ctx, DefaultAccount)
	if err := requireIntegrationMigration(err, "legacy default"); err != nil {
		return err
	}
	if err := store.MigrateKeychain(ctx, integrationAccount); err != nil {
		return err
	}
	credentials, err := store.Load(ctx, integrationAccount)
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

func requireIntegrationMigration(err error, kind string) error {
	if err == nil {
		return errors.New(kind + " creator-scoped integration item was readable before migration")
	}
	var typed *errx.Error
	reason := "an untyped error"
	typedError := errors.As(err, &typed)
	if typedError && typed.Reason != "" {
		reason = typed.Reason
	}
	if !typedError || typed.Reason != "KEYRING_MIGRATION_REQUIRED" {
		var statusErr *keychainStatusError
		if errors.As(err, &statusErr) {
			return fmt.Errorf("%s creator-scoped integration item returned %s (OSStatus %d) before migration", kind, reason, statusErr.status)
		}
		return fmt.Errorf("%s creator-scoped integration item returned %s before migration", kind, reason)
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
