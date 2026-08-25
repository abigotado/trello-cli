//go:build darwin && cgo

package auth

/*
#cgo LDFLAGS: -framework CoreFoundation -framework Security
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>

static CFMutableDictionaryRef trello_keychain_query(CFStringRef service, CFStringRef account, SecKeychainRef keychain, Boolean search) {
	CFMutableDictionaryRef query = CFDictionaryCreateMutable(kCFAllocatorDefault, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (query == NULL) return NULL;
	CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(query, kSecAttrService, service);
	CFDictionarySetValue(query, kSecAttrAccount, account);
	CFDictionarySetValue(query, kSecUseAuthenticationUI, kSecUseAuthenticationUIFail);
	if (keychain != NULL && search) {
		const void *values[] = {keychain};
		CFArrayRef searchList = CFArrayCreate(kCFAllocatorDefault, values, 1, &kCFTypeArrayCallBacks);
		if (searchList == NULL) {
			CFRelease(query);
			return NULL;
		}
		CFDictionarySetValue(query, kSecMatchSearchList, searchList);
		CFRelease(searchList);
	} else if (keychain != NULL) {
		CFDictionarySetValue(query, kSecUseKeychain, keychain);
	}
	return query;
}

static OSStatus trello_keychain_get(CFStringRef service, CFStringRef account, SecKeychainRef keychain, CFTypeRef *result) {
	CFMutableDictionaryRef query = trello_keychain_query(service, account, keychain, true);
	if (query == NULL) return errSecAllocate;
	CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);
	CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
	OSStatus status = SecItemCopyMatching(query, result);
	CFRelease(query);
	return status;
}

static OSStatus trello_keychain_resolve_item(CFStringRef service, CFStringRef account, SecKeychainRef keychain, CFTypeRef *result) {
	CFMutableDictionaryRef query = trello_keychain_query(service, account, keychain, true);
	if (query == NULL) return errSecAllocate;
	CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);
	CFDictionarySetValue(query, kSecReturnRef, kCFBooleanTrue);
	OSStatus status = SecItemCopyMatching(query, result);
	CFRelease(query);
	return status;
}

static CFMutableDictionaryRef trello_keychain_item_query(SecKeychainItemRef item, SecKeychainRef keychain) {
	CFMutableDictionaryRef query = CFDictionaryCreateMutable(kCFAllocatorDefault, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (query == NULL) return NULL;
	const void *values[] = {item};
	CFArrayRef items = CFArrayCreate(kCFAllocatorDefault, values, 1, &kCFTypeArrayCallBacks);
	if (items == NULL) {
		CFRelease(query);
		return NULL;
	}
	CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(query, kSecMatchItemList, items);
	CFDictionarySetValue(query, kSecUseAuthenticationUI, kSecUseAuthenticationUIFail);
	CFRelease(items);
	if (keychain != NULL) {
		const void *keychains[] = {keychain};
		CFArrayRef searchList = CFArrayCreate(kCFAllocatorDefault, keychains, 1, &kCFTypeArrayCallBacks);
		if (searchList == NULL) {
			CFRelease(query);
			return NULL;
		}
		CFDictionarySetValue(query, kSecMatchSearchList, searchList);
		CFRelease(searchList);
	}
	return query;
}

static OSStatus trello_keychain_update_item(SecKeychainItemRef item, SecKeychainRef keychain, CFDataRef value) {
	CFMutableDictionaryRef query = trello_keychain_item_query(item, keychain);
	if (query == NULL) return errSecAllocate;
	const void *keys[] = {kSecValueData};
	const void *values[] = {value};
	CFDictionaryRef attributes = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	if (attributes == NULL) {
		CFRelease(query);
		return errSecAllocate;
	}
	OSStatus status = SecItemUpdate(query, attributes);
	CFRelease(attributes);
	CFRelease(query);
	return status;
}

static OSStatus trello_keychain_delete_item(SecKeychainItemRef item, SecKeychainRef keychain) {
	CFMutableDictionaryRef query = trello_keychain_item_query(item, keychain);
	if (query == NULL) return errSecAllocate;
	OSStatus status = SecItemDelete(query);
	CFRelease(query);
	return status;
}

static OSStatus trello_keychain_add(CFStringRef service, CFStringRef account, SecKeychainRef keychain, CFDataRef value, SecAccessRef access) {
	CFMutableDictionaryRef attributes = trello_keychain_query(service, account, keychain, false);
	if (attributes == NULL) return errSecAllocate;
	CFDictionarySetValue(attributes, kSecValueData, value);
	CFDictionarySetValue(attributes, kSecAttrAccess, access);
	OSStatus status = SecItemAdd(attributes, NULL);
	CFRelease(attributes);
	return status;
}

static OSStatus trello_keychain_create_access(CFStringRef description, SecAccessRef *access) {
	CFArrayRef trustedApplications = CFArrayCreate(kCFAllocatorDefault, NULL, 0, &kCFTypeArrayCallBacks);
	if (trustedApplications == NULL) return errSecAllocate;
	OSStatus status = SecAccessCreate(description, trustedApplications, access);
	CFRelease(trustedApplications);
	return status;
}

static OSStatus trello_keychain_copy_decrypt_acl(SecAccessRef access, CFArrayRef *aclList, SecACLRef *acl, CFArrayRef *applicationList, CFStringRef *description, SecKeychainPromptSelector *promptSelector, CFIndex *count) {
	*aclList = SecAccessCopyMatchingACLList(access, kSecACLAuthorizationDecrypt);
	if (*aclList == NULL) return errSecInternalComponent;
	*count = CFArrayGetCount(*aclList);
	if (*count != 1) return errSecSuccess;
	*acl = (SecACLRef)CFArrayGetValueAtIndex(*aclList, 0);
	return SecACLCopyContents(*acl, applicationList, description, promptSelector);
}

static OSStatus trello_keychain_set_allow_any_acl(SecACLRef acl, CFStringRef description, SecKeychainPromptSelector promptSelector) {
	return SecACLSetContents(acl, NULL, description, promptSelector);
}
*/
import "C"

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unsafe"
)

const (
	goKeyringHexPrefix    = "go-keyring-encoded:"
	goKeyringBase64Prefix = "go-keyring-base64:"
	maxKeychainItemBytes  = 64 * 1024
	// cgo exposes Security.framework reference typedefs as integer-sized
	// opaque handles on Darwin. Their Go null sentinel is zero, not nil.
	nullSecurityRef = 0

	keychainStatusSuccess       int64  = int64(C.errSecSuccess)
	keychainStatusDuplicateItem int64  = int64(C.errSecDuplicateItem)
	keychainStatusItemNotFound  int64  = int64(C.errSecItemNotFound)
	keychainStatusNoInteraction int64  = int64(C.errSecInteractionNotAllowed)
	keychainStatusUserCanceled  int64  = int64(C.errSecUserCanceled)
	keychainPromptRequirePass   uint16 = uint16(C.kSecKeychainPromptRequirePassphase)
)

var (
	errKeyringDuplicate          = errors.New("keyring item already exists")
	errMalformedKeychainEncoding = errors.New("keychain item uses malformed go-keyring encoding")
)

type securityFrameworkBackend struct {
	searchKeychain C.SecKeychainRef
	addKeychain    C.SecKeychainRef
}

type keychainStatusError struct {
	operation string
	status    int64
}

func (e *keychainStatusError) Error() string {
	return fmt.Sprintf("keychain %s failed with OSStatus %d", e.operation, e.status)
}

type decryptACLCountError struct{ count int }

func (e *decryptACLCountError) Error() string {
	return fmt.Sprintf("keychain access has %d decrypt ACL entries; expected exactly one", e.count)
}

type keychainItemPresence struct {
	primary bool
	legacy  bool
}

type loginSaveOperations struct {
	evaluate        func() (keychainItemPresence, error)
	normalizeLegacy func() error
	writePrimary    func() error
	writeLegacy     func() error
	addPrimary      func() error
}

type resolveKeychainItem[T any] func() (T, func(), int64)

type migrateKeychainItem func() (bool, error)

func platformKeyringBackend() keyringBackend { return securityFrameworkBackend{} }

func (backend securityFrameworkBackend) get(ctx context.Context, service, account string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	serviceRef, releaseService, err := makeCFString(service)
	if err != nil {
		return "", err
	}
	defer releaseService()
	accountRef, releaseAccount, err := makeCFString(account)
	if err != nil {
		return "", err
	}
	defer releaseAccount()

	var result C.CFTypeRef
	status := C.trello_keychain_get(serviceRef, accountRef, backend.searchKeychain, &result)
	if status != C.errSecSuccess {
		releaseCFType(result)
		return "", translateKeychainStatus("read", int64(status))
	}
	if result == nullSecurityRef {
		return "", internalKeychainError("read")
	}
	defer C.CFRelease(result)
	if C.CFGetTypeID(result) != C.CFDataGetTypeID() {
		return "", errors.New("keychain read returned an unexpected value type")
	}
	data := C.CFDataRef(result)
	length := C.CFDataGetLength(data)
	if err := validateKeychainItemLength(int64(length)); err != nil {
		return "", err
	}
	if length == 0 {
		return decodeGoKeyringValue("")
	}
	bytes := C.CFDataGetBytePtr(data)
	if bytes == nil {
		return "", internalKeychainError("read")
	}
	raw := C.GoBytes(unsafe.Pointer(bytes), C.int(length))
	return decodeGoKeyringValue(string(raw))
}

func (backend securityFrameworkBackend) set(ctx context.Context, service, account, value string) error {
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
	var (
		addErr     error
		contextErr error
	)
	err = updateOrAddKeychainItem(
		func() int64 {
			if err := ctx.Err(); err != nil {
				contextErr = err
				return keychainStatusUserCanceled
			}
			return withResolvedKeychainItem(
				backend.resolveItemHandle(refs.service, refs.account),
				func(item C.SecKeychainItemRef) int64 {
					return int64(C.trello_keychain_update_item(item, backend.searchKeychain, refs.value))
				},
			)
		},
		func() int64 {
			if err := ctx.Err(); err != nil {
				contextErr = err
				return keychainStatusUserCanceled
			}
			access, releaseAccess, accessErr := makeAllowAnyAccess(refs.service)
			if accessErr != nil {
				addErr = accessErr
				return int64(C.errSecInternalComponent)
			}
			defer releaseAccess()
			return int64(C.trello_keychain_add(refs.service, refs.account, backend.addKeychain, refs.value, access))
		},
	)
	if addErr != nil {
		return addErr
	}
	if contextErr != nil {
		return contextErr
	}
	return err
}

func (backend securityFrameworkBackend) setForLogin(ctx context.Context, service, primaryAccount, legacyAccount, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := encodeGoKeyringValue(value)
	if err != nil {
		return err
	}
	primary, releasePrimary, err := makeKeychainWriteRefs(service, primaryAccount, encoded)
	if err != nil {
		return err
	}
	defer releasePrimary()
	legacyRef, releaseLegacy, err := makeOptionalCFString(legacyAccount)
	if err != nil {
		return err
	}
	defer releaseLegacy()

	operations := loginSaveOperations{
		evaluate: func() (keychainItemPresence, error) {
			return backend.evaluatePresence(ctx, primary.service, primary.account, legacyRef)
		},
		normalizeLegacy: func() error {
			return backend.normalizeExisting(ctx, primary.service, legacyRef)
		},
		writePrimary: func() error {
			return backend.writeExistingForLogin(ctx, primary.service, primary.account, primary.value)
		},
		writeLegacy: func() error {
			return backend.writeExistingForLogin(ctx, primary.service, legacyRef, primary.value)
		},
		addPrimary: func() error {
			return backend.addForLogin(ctx, primary)
		},
	}
	return saveForLoginWithPolicy(operations)
}

func (backend securityFrameworkBackend) migrate(ctx context.Context, service, primaryAccount, legacyAccount string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	serviceRef, releaseService, err := makeCFString(service)
	if err != nil {
		return err
	}
	defer releaseService()
	primaryRef, releasePrimary, err := makeCFString(primaryAccount)
	if err != nil {
		return err
	}
	defer releasePrimary()
	legacyRef, releaseLegacy, err := makeOptionalCFString(legacyAccount)
	if err != nil {
		return err
	}
	defer releaseLegacy()

	var migrateLegacy migrateKeychainItem
	if legacyRef != nullSecurityRef {
		migrateLegacy = func() (bool, error) {
			return backend.migrateExactItem(ctx, serviceRef, legacyRef)
		}
	}
	return migrateKeychainItems(
		func() (bool, error) {
			return backend.migrateExactItem(ctx, serviceRef, primaryRef)
		},
		migrateLegacy,
	)
}

func (backend securityFrameworkBackend) delete(ctx context.Context, service, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	serviceRef, releaseService, err := makeCFString(service)
	if err != nil {
		return err
	}
	defer releaseService()
	accountRef, releaseAccount, err := makeCFString(account)
	if err != nil {
		return err
	}
	defer releaseAccount()

	return deleteResolvedKeychainItem(
		backend.resolveItemHandle(serviceRef, accountRef),
		func(item C.SecKeychainItemRef) int64 {
			return int64(C.trello_keychain_delete_item(item, backend.searchKeychain))
		},
	)
}

type keychainWriteRefs struct {
	service C.CFStringRef
	account C.CFStringRef
	value   C.CFDataRef
}

func makeKeychainWriteRefs(service, account, value string) (keychainWriteRefs, func(), error) {
	serviceRef, releaseService, err := makeCFString(service)
	if err != nil {
		return keychainWriteRefs{}, func() {}, err
	}
	accountRef, releaseAccount, err := makeCFString(account)
	if err != nil {
		releaseService()
		return keychainWriteRefs{}, func() {}, err
	}
	valueRef := C.CFDataCreate(C.kCFAllocatorDefault, (*C.UInt8)(unsafe.Pointer(unsafe.StringData(value))), C.CFIndex(len(value)))
	if valueRef == nullSecurityRef {
		releaseAccount()
		releaseService()
		return keychainWriteRefs{}, func() {}, internalKeychainError("write")
	}
	refs := keychainWriteRefs{service: serviceRef, account: accountRef, value: valueRef}
	return refs, func() {
		C.CFRelease(C.CFTypeRef(valueRef))
		releaseAccount()
		releaseService()
	}, nil
}

func makeCFString(value string) (C.CFStringRef, func(), error) {
	result := C.CFStringCreateWithBytes(C.kCFAllocatorDefault, (*C.UInt8)(unsafe.Pointer(unsafe.StringData(value))), C.CFIndex(len(value)), C.kCFStringEncodingUTF8, C.false)
	if result == nullSecurityRef {
		return nullSecurityRef, func() {}, internalKeychainError("allocate")
	}
	return result, func() { C.CFRelease(C.CFTypeRef(result)) }, nil
}

func makeOptionalCFString(value string) (C.CFStringRef, func(), error) {
	if value == "" {
		return nullSecurityRef, func() {}, nil
	}
	return makeCFString(value)
}

func makeAllowAnyAccess(description C.CFStringRef) (C.SecAccessRef, func(), error) {
	var access C.SecAccessRef
	status := C.trello_keychain_create_access(description, &access)
	if status != C.errSecSuccess {
		releaseCFType(C.CFTypeRef(access))
		return nullSecurityRef, func() {}, translateKeychainStatus("create access", int64(status))
	}
	if access == nullSecurityRef {
		return nullSecurityRef, func() {}, internalKeychainError("create access")
	}
	if _, err := normalizeAllowAnyAccess(access); err != nil {
		C.CFRelease(C.CFTypeRef(access))
		return nullSecurityRef, func() {}, err
	}
	return access, func() { C.CFRelease(C.CFTypeRef(access)) }, nil
}

func normalizeAllowAnyAccess(access C.SecAccessRef) (bool, error) {
	var aclList C.CFArrayRef
	var acl C.SecACLRef
	var applicationList C.CFArrayRef
	var description C.CFStringRef
	var promptSelector C.SecKeychainPromptSelector
	var count C.CFIndex
	status := C.trello_keychain_copy_decrypt_acl(access, &aclList, &acl, &applicationList, &description, &promptSelector, &count)
	defer releaseCFType(C.CFTypeRef(aclList))
	defer releaseCFType(C.CFTypeRef(applicationList))
	defer releaseCFType(C.CFTypeRef(description))
	if status != C.errSecSuccess {
		return false, translateKeychainStatus("read access", int64(status))
	}
	flags, changed, err := normalizeAllowAnyACL(int(count), applicationList == nullSecurityRef, uint16(promptSelector))
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	status = C.trello_keychain_set_allow_any_acl(acl, description, C.SecKeychainPromptSelector(flags))
	return true, translateKeychainStatus("normalize access", int64(status))
}

func normalizeAllowAnyACL(decryptACLCount int, applicationListIsNil bool, promptFlags uint16) (uint16, bool, error) {
	if decryptACLCount != 1 {
		return 0, false, &decryptACLCountError{count: decryptACLCount}
	}
	normalizedFlags := promptFlags &^ keychainPromptRequirePass
	changed := !applicationListIsNil || normalizedFlags != promptFlags
	return normalizedFlags, changed, nil
}

func (backend securityFrameworkBackend) migrateExistingAccess(item C.SecKeychainItemRef) error {
	var access C.SecAccessRef
	status := C.SecKeychainItemCopyAccess(item, &access)
	if status != C.errSecSuccess {
		releaseCFType(C.CFTypeRef(access))
		return translateKeychainStatus("copy access", int64(status))
	}
	if access == nullSecurityRef {
		return internalKeychainError("copy access")
	}
	defer C.CFRelease(C.CFTypeRef(access))
	changed, err := normalizeAllowAnyAccess(access)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	status = C.SecKeychainItemSetAccess(item, access)
	return translateKeychainStatus("set access", int64(status))
}

func (backend securityFrameworkBackend) migrateExactItem(ctx context.Context, service, account C.CFStringRef) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	item, releaseItem, status := backend.resolveItem(service, account)
	switch status {
	case keychainStatusItemNotFound:
		return false, nil
	case keychainStatusSuccess:
		defer releaseItem()
	default:
		return false, translateKeychainStatus("resolve", status)
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	return true, backend.migrateExistingAccess(item)
}

func (backend securityFrameworkBackend) resolveItem(service, account C.CFStringRef) (C.SecKeychainItemRef, func(), int64) {
	var result C.CFTypeRef
	status := C.trello_keychain_resolve_item(service, account, backend.searchKeychain, &result)
	if status != C.errSecSuccess {
		releaseCFType(result)
		return nullSecurityRef, func() {}, int64(status)
	}
	if result == nullSecurityRef || C.CFGetTypeID(result) != C.SecKeychainItemGetTypeID() {
		releaseCFType(result)
		return nullSecurityRef, func() {}, int64(C.errSecInternalComponent)
	}
	item := C.SecKeychainItemRef(result)
	return item, func() { C.CFRelease(result) }, keychainStatusSuccess
}

func (backend securityFrameworkBackend) resolveItemHandle(service, account C.CFStringRef) resolveKeychainItem[C.SecKeychainItemRef] {
	return func() (C.SecKeychainItemRef, func(), int64) {
		return backend.resolveItem(service, account)
	}
}

func withResolvedKeychainItem[T any](resolve resolveKeychainItem[T], operation func(T) int64) int64 {
	handle, release, status := resolve()
	if status != keychainStatusSuccess {
		return status
	}
	defer release()
	return operation(handle)
}

func deleteResolvedKeychainItem[T any](resolve resolveKeychainItem[T], deleteItem func(T) int64) error {
	status := withResolvedKeychainItem(resolve, deleteItem)
	if status == keychainStatusItemNotFound {
		return nil
	}
	return translateKeychainStatus("delete", status)
}

func (backend securityFrameworkBackend) itemExists(service, account C.CFStringRef) (bool, error) {
	item, releaseItem, status := backend.resolveItem(service, account)
	if item != nullSecurityRef {
		releaseItem()
	}
	switch status {
	case keychainStatusSuccess:
		return true, nil
	case keychainStatusItemNotFound:
		return false, nil
	default:
		return false, translateKeychainStatus("resolve", status)
	}
}

func (backend securityFrameworkBackend) evaluatePresence(ctx context.Context, service, primaryAccount, legacyAccount C.CFStringRef) (keychainItemPresence, error) {
	if err := ctx.Err(); err != nil {
		return keychainItemPresence{}, err
	}
	primary, err := backend.itemExists(service, primaryAccount)
	if err != nil {
		return keychainItemPresence{}, err
	}
	if legacyAccount == nullSecurityRef {
		return keychainItemPresence{primary: primary}, nil
	}
	legacy, err := backend.itemExists(service, legacyAccount)
	if err != nil {
		return keychainItemPresence{}, err
	}
	return keychainItemPresence{primary: primary, legacy: legacy}, nil
}

func (backend securityFrameworkBackend) normalizeExisting(ctx context.Context, service, account C.CFStringRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	item, releaseItem, status := backend.resolveItem(service, account)
	if status != keychainStatusSuccess {
		return translateKeychainStatus("resolve", status)
	}
	defer releaseItem()
	return backend.migrateExistingAccess(item)
}

func (backend securityFrameworkBackend) writeExistingForLogin(ctx context.Context, service, account C.CFStringRef, value C.CFDataRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	item, releaseItem, status := backend.resolveItem(service, account)
	if status != keychainStatusSuccess {
		return translateKeychainStatus("resolve", status)
	}
	defer releaseItem()
	if err := backend.migrateExistingAccess(item); err != nil {
		return err
	}
	status = int64(C.trello_keychain_update_item(item, backend.searchKeychain, value))
	return translateKeychainStatus("write", status)
}

func (backend securityFrameworkBackend) addForLogin(ctx context.Context, refs keychainWriteRefs) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	access, releaseAccess, err := makeAllowAnyAccess(refs.service)
	if err != nil {
		return err
	}
	defer releaseAccess()
	status := int64(C.trello_keychain_add(refs.service, refs.account, backend.addKeychain, refs.value, access))
	return translateKeychainStatus("write", status)
}

func saveForLoginWithPolicy(operations loginSaveOperations) error {
	for attempt := 0; attempt < 2; attempt++ {
		presence, err := operations.evaluate()
		if err != nil {
			return err
		}
		err = applyLoginSavePolicy(presence, operations)
		if err == nil {
			return nil
		}
		if attempt == 0 && isKeychainRace(err) {
			continue
		}
		return err
	}
	return internalKeychainError("write")
}

func applyLoginSavePolicy(presence keychainItemPresence, operations loginSaveOperations) error {
	switch {
	case presence.primary && presence.legacy:
		if err := operations.normalizeLegacy(); err != nil {
			return err
		}
		return operations.writePrimary()
	case presence.primary:
		return operations.writePrimary()
	case presence.legacy:
		return operations.writeLegacy()
	default:
		return operations.addPrimary()
	}
}

func isKeychainRace(err error) bool {
	return errors.Is(err, errKeyringNotFound) || errors.Is(err, errKeyringDuplicate)
}

func migrateKeychainItems(primary, legacy migrateKeychainItem) error {
	found := false
	for _, migrate := range []migrateKeychainItem{primary, legacy} {
		if migrate == nil {
			continue
		}
		exists, err := migrate()
		if err != nil {
			return err
		}
		found = found || exists
	}
	if !found {
		return errKeyringNotFound
	}
	return nil
}

func translateKeychainStatus(operation string, status int64) error {
	switch status {
	case keychainStatusSuccess:
		return nil
	case keychainStatusDuplicateItem:
		return errKeyringDuplicate
	case keychainStatusItemNotFound:
		return errKeyringNotFound
	case keychainStatusNoInteraction:
		return errKeyringInteractionNotAllowed
	case keychainStatusUserCanceled:
		return errKeyringUserCanceled
	default:
		return &keychainStatusError{operation: operation, status: status}
	}
}

func encodeGoKeyringValue(value string) (string, error) {
	if len(value) > maxKeychainItemBytes {
		return "", errors.New("keychain item exceeds the safe size limit")
	}
	encoded := goKeyringBase64Prefix + base64.StdEncoding.EncodeToString([]byte(value))
	if err := validateKeychainItemLength(int64(len(encoded))); err != nil {
		return "", err
	}
	return encoded, nil
}

func validateKeychainItemLength(length int64) error {
	if length < 0 {
		return errors.New("keychain item has an invalid size")
	}
	if length > maxKeychainItemBytes {
		return errors.New("keychain item exceeds the safe size limit")
	}
	return nil
}

func updateOrAddKeychainItem(update, add func() int64) error {
	status := update()
	if status == keychainStatusSuccess {
		return nil
	}
	if status != keychainStatusItemNotFound {
		return translateKeychainStatus("write", status)
	}

	status = add()
	if status == keychainStatusDuplicateItem {
		status = update()
	}
	return translateKeychainStatus("write", status)
}

func decodeGoKeyringValue(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(trimmed, goKeyringHexPrefix):
		decoded, err := hex.DecodeString(strings.TrimPrefix(trimmed, goKeyringHexPrefix))
		if err != nil {
			return "", errMalformedKeychainEncoding
		}
		return string(decoded), nil
	case strings.HasPrefix(trimmed, goKeyringBase64Prefix):
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(trimmed, goKeyringBase64Prefix))
		if err != nil {
			return "", errMalformedKeychainEncoding
		}
		return string(decoded), nil
	default:
		return trimmed, nil
	}
}

func internalKeychainError(operation string) error {
	return &keychainStatusError{operation: operation, status: int64(C.errSecInternalComponent)}
}

func releaseCFType(value C.CFTypeRef) {
	if value != nullSecurityRef {
		C.CFRelease(value)
	}
}
