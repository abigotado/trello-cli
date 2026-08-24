//go:build darwin && cgo

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDecodeGoKeyringValueCompatibility(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "raw value is trimmed",
			value: "  raw-value\n",
			want:  "raw-value",
		},
		{
			name:  "legacy hex encoding is decoded",
			value: "\n go-keyring-encoded:7b226170695f6b6579223a2261222c22746f6b656e223a2262227d \t",
			want:  `{"api_key":"a","token":"b"}`,
		},
		{
			name:  "base64 encoding is decoded",
			value: " go-keyring-base64:eyJhcGlfa2V5IjoiYSIsInRva2VuIjoiYiJ9 ",
			want:  `{"api_key":"a","token":"b"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeGoKeyringValue(tt.value)
			if err != nil {
				t.Fatalf("decodeGoKeyringValue() error = %v", err)
			}
			if got != tt.want {
				t.Error("decodeGoKeyringValue() returned the wrong logical value")
			}
		})
	}
}

func TestMalformedKnownEncodingIsAKeyringFailure(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "malformed hex", value: goKeyringHexPrefix + "not-hex"},
		{name: "malformed base64", value: goKeyringBase64Prefix + "%%%"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, decodeErr := decodeGoKeyringValue(tt.value)
			if !errors.Is(decodeErr, errMalformedKeychainEncoding) {
				t.Fatalf("decode error = %v, want errMalformedKeychainEncoding", decodeErr)
			}

			entry := backendEntry{service: KeyringService, account: entryName("work")}
			backend := newFakeKeyringBackend(nil)
			backend.getErrors[entry] = decodeErr
			_, err := (KeyringStore{backend: backend}).Load(context.Background(), "work")
			assertAuthError(t, err, "KEYRING_UNAVAILABLE")
			if strings.Contains(err.Error(), tt.value) {
				t.Error("keyring error disclosed the malformed stored value")
			}
		})
	}
}

func TestEncodeGoKeyringValueUsesCompatibleBase64WireFormat(t *testing.T) {
	const logical = `{"api_key":"a","token":"b"}`
	const wire = "go-keyring-base64:eyJhcGlfa2V5IjoiYSIsInRva2VuIjoiYiJ9"

	got, err := encodeGoKeyringValue(logical)
	if err != nil {
		t.Fatalf("encodeGoKeyringValue() error = %v", err)
	}
	if got != wire {
		t.Error("encodeGoKeyringValue() changed the compatible wire format")
	}
	roundTrip, err := decodeGoKeyringValue(got)
	if err != nil {
		t.Fatalf("decodeGoKeyringValue(encoded) error = %v", err)
	}
	if roundTrip != logical {
		t.Error("encoded keychain value did not round-trip")
	}
}

func TestKeychainItemSizeBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		length  int64
		wantErr bool
	}{
		{name: "negative length is invalid", length: -1, wantErr: true},
		{name: "empty value is allowed", length: 0},
		{name: "maximum length is allowed", length: maxKeychainItemBytes},
		{name: "one byte over maximum is rejected", length: maxKeychainItemBytes + 1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateKeychainItemLength(tt.length)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateKeychainItemLength() error presence = %v, want %v", err != nil, tt.wantErr)
			}
		})
	}

	if _, err := encodeGoKeyringValue(strings.Repeat("x", maxKeychainItemBytes+1)); err == nil {
		t.Error("encodeGoKeyringValue() accepted an oversized logical value")
	}
	if _, err := encodeGoKeyringValue(strings.Repeat("x", maxKeychainItemBytes)); err == nil {
		t.Error("encodeGoKeyringValue() accepted wire data that expands past the size limit")
	}
}

func TestUpdateOrAddKeychainItem(t *testing.T) {
	const (
		genericFailure int64  = -50
		secretSentinel string = "state-machine-secret-must-not-appear"
	)
	type updateAttempt struct {
		resolveStatus int64
		updateStatus  int64
	}
	tests := []struct {
		name             string
		updates          []updateAttempt
		addStatuses      []int64
		wantUpdateCalls  int
		wantUpdateOps    int
		wantReleaseCalls int
		wantAddCalls     int
		wantError        error
		wantStatus       *int64
	}{
		{
			name: "existing item updates before add and stops",
			updates: []updateAttempt{{
				resolveStatus: keychainStatusSuccess,
				updateStatus:  keychainStatusSuccess,
			}},
			wantUpdateCalls:  1,
			wantUpdateOps:    1,
			wantReleaseCalls: 1,
		},
		{
			name: "update resolve not-found adds once",
			updates: []updateAttempt{{
				resolveStatus: keychainStatusItemNotFound,
			}},
			addStatuses:     []int64{keychainStatusSuccess},
			wantUpdateCalls: 1,
			wantAddCalls:    1,
		},
		{
			name: "resolved item disappearing during update adds once",
			updates: []updateAttempt{{
				resolveStatus: keychainStatusSuccess,
				updateStatus:  keychainStatusItemNotFound,
			}},
			addStatuses:      []int64{keychainStatusSuccess},
			wantUpdateCalls:  1,
			wantUpdateOps:    1,
			wantReleaseCalls: 1,
			wantAddCalls:     1,
		},
		{
			name: "duplicate add re-resolves and updates the new exact item",
			updates: []updateAttempt{
				{resolveStatus: keychainStatusItemNotFound},
				{resolveStatus: keychainStatusSuccess, updateStatus: keychainStatusSuccess},
			},
			addStatuses:      []int64{keychainStatusDuplicateItem},
			wantUpdateCalls:  2,
			wantUpdateOps:    1,
			wantReleaseCalls: 1,
			wantAddCalls:     1,
		},
		{
			name: "final disappearance after duplicate add is not retried",
			updates: []updateAttempt{
				{resolveStatus: keychainStatusItemNotFound},
				{resolveStatus: keychainStatusSuccess, updateStatus: keychainStatusItemNotFound},
			},
			addStatuses:      []int64{keychainStatusDuplicateItem},
			wantUpdateCalls:  2,
			wantUpdateOps:    1,
			wantReleaseCalls: 1,
			wantAddCalls:     1,
			wantError:        errKeyringNotFound,
		},
		{
			name: "generic update failure does not add or retry",
			updates: []updateAttempt{{
				resolveStatus: keychainStatusSuccess,
				updateStatus:  genericFailure,
			}},
			wantUpdateCalls:  1,
			wantUpdateOps:    1,
			wantReleaseCalls: 1,
			wantStatus:       int64Pointer(genericFailure),
		},
		{
			name: "generic add failure does not retry",
			updates: []updateAttempt{{
				resolveStatus: keychainStatusItemNotFound,
			}},
			addStatuses:     []int64{genericFailure},
			wantUpdateCalls: 1,
			wantAddCalls:    1,
			wantStatus:      int64Pointer(genericFailure),
		},
		{
			name: "generic final update failure after duplicate is bounded",
			updates: []updateAttempt{
				{resolveStatus: keychainStatusItemNotFound},
				{resolveStatus: keychainStatusSuccess, updateStatus: genericFailure},
			},
			addStatuses:      []int64{keychainStatusDuplicateItem},
			wantUpdateCalls:  2,
			wantUpdateOps:    1,
			wantReleaseCalls: 1,
			wantAddCalls:     1,
			wantStatus:       int64Pointer(genericFailure),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updateCalls := 0
			updateOps := 0
			releaseCalls := 0
			addCalls := 0
			handles := make([]*int, len(tt.updates))
			for i := range handles {
				identity := i + 1
				handles[i] = &identity
			}

			err := updateOrAddKeychainItem(
				func() int64 {
					attemptIndex := updateCalls
					attempt := tt.updates[attemptIndex]
					wantHandle := handles[attemptIndex]
					updateCalls++
					return withResolvedKeychainItem(
						func() (*int, func(), int64) {
							return wantHandle, func() { releaseCalls++ }, attempt.resolveStatus
						},
						func(got *int) int64 {
							updateOps++
							if got != wantHandle {
								t.Error("update received a different resolved item identity")
							}
							return attempt.updateStatus
						},
					)
				},
				func() int64 {
					status := tt.addStatuses[addCalls]
					addCalls++
					return status
				},
			)

			if updateCalls != tt.wantUpdateCalls || updateOps != tt.wantUpdateOps || releaseCalls != tt.wantReleaseCalls || addCalls != tt.wantAddCalls {
				t.Errorf(
					"update/update-op/release/add calls = %d/%d/%d/%d, want %d/%d/%d/%d",
					updateCalls,
					updateOps,
					releaseCalls,
					addCalls,
					tt.wantUpdateCalls,
					tt.wantUpdateOps,
					tt.wantReleaseCalls,
					tt.wantAddCalls,
				)
			}
			switch {
			case tt.wantError != nil:
				if !errors.Is(err, tt.wantError) {
					t.Errorf("error = %v, want %v", err, tt.wantError)
				}
			case tt.wantStatus != nil:
				var statusErr *keychainStatusError
				if !errors.As(err, &statusErr) {
					t.Fatalf("error = %v, want *keychainStatusError", err)
				}
				if statusErr.operation != "write" || statusErr.status != *tt.wantStatus {
					t.Errorf("status error = %q/%d, want write/%d", statusErr.operation, statusErr.status, *tt.wantStatus)
				}
			case err != nil:
				t.Errorf("updateOrAddKeychainItem() error = %v", err)
			}
			if err != nil && strings.Contains(err.Error(), secretSentinel) {
				t.Error("state-machine error disclosed the secret sentinel")
			}
		})
	}
}

func int64Pointer(value int64) *int64 { return &value }

func TestApplyLoginSavePolicy(t *testing.T) {
	tests := []struct {
		name     string
		presence keychainItemPresence
		want     []string
	}{
		{
			name:     "primary and legacy normalize legacy before updating primary",
			presence: keychainItemPresence{primary: true, legacy: true},
			want:     []string{"normalizeLegacy", "writePrimary"},
		},
		{
			name:     "primary only updates primary",
			presence: keychainItemPresence{primary: true},
			want:     []string{"writePrimary"},
		},
		{
			name:     "legacy only normalizes and updates legacy in place",
			presence: keychainItemPresence{legacy: true},
			want:     []string{"writeLegacy"},
		},
		{
			name: "neither adds primary",
			want: []string{"addPrimary"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			operations := loginSaveOperations{
				normalizeLegacy: recordLoginOperation(&got, "normalizeLegacy", nil),
				writePrimary:    recordLoginOperation(&got, "writePrimary", nil),
				writeLegacy:     recordLoginOperation(&got, "writeLegacy", nil),
				addPrimary:      recordLoginOperation(&got, "addPrimary", nil),
			}
			if err := applyLoginSavePolicy(tt.presence, operations); err != nil {
				t.Fatalf("applyLoginSavePolicy() error = %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("operations = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyLoginSavePolicyStopsAtFirstFailure(t *testing.T) {
	operationFailure := errors.New("operation unavailable")
	var got []string
	operations := loginSaveOperations{
		normalizeLegacy: recordLoginOperation(&got, "normalizeLegacy", operationFailure),
		writePrimary:    recordLoginOperation(&got, "writePrimary", nil),
		writeLegacy:     recordLoginOperation(&got, "writeLegacy", nil),
		addPrimary:      recordLoginOperation(&got, "addPrimary", nil),
	}

	err := applyLoginSavePolicy(keychainItemPresence{primary: true, legacy: true}, operations)
	if !errors.Is(err, operationFailure) {
		t.Errorf("error = %v, want operation failure", err)
	}
	if strings.Join(got, ",") != "normalizeLegacy" {
		t.Errorf("operations after failure = %v, want only normalizeLegacy", got)
	}
}

func TestSaveForLoginWithPolicyRetriesOnlyOneWholeStateRace(t *testing.T) {
	operationFailure := errors.New("operation unavailable")
	tests := []struct {
		name            string
		operationErrors []error
		wantEvaluations int
		wantOperations  int
		wantError       error
	}{
		{
			name:            "not-found race reevaluates once then succeeds",
			operationErrors: []error{errKeyringNotFound, nil},
			wantEvaluations: 2,
			wantOperations:  2,
		},
		{
			name:            "duplicate race reevaluates once then succeeds",
			operationErrors: []error{errKeyringDuplicate, nil},
			wantEvaluations: 2,
			wantOperations:  2,
		},
		{
			name:            "persistent race is bounded to two evaluations",
			operationErrors: []error{errKeyringNotFound, errKeyringDuplicate},
			wantEvaluations: 2,
			wantOperations:  2,
			wantError:       errKeyringDuplicate,
		},
		{
			name:            "generic failure is not retried",
			operationErrors: []error{operationFailure},
			wantEvaluations: 1,
			wantOperations:  1,
			wantError:       operationFailure,
		},
		{
			name:            "cancellation is not retried",
			operationErrors: []error{context.Canceled},
			wantEvaluations: 1,
			wantOperations:  1,
			wantError:       context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evaluations := 0
			operations := 0
			err := saveForLoginWithPolicy(loginSaveOperations{
				evaluate: func() (keychainItemPresence, error) {
					evaluations++
					return keychainItemPresence{}, nil
				},
				addPrimary: func() error {
					err := tt.operationErrors[operations]
					operations++
					return err
				},
			})
			if evaluations != tt.wantEvaluations || operations != tt.wantOperations {
				t.Errorf("evaluations/operations = %d/%d, want %d/%d", evaluations, operations, tt.wantEvaluations, tt.wantOperations)
			}
			if tt.wantError == nil && err != nil {
				t.Errorf("saveForLoginWithPolicy() error = %v", err)
			}
			if tt.wantError != nil && !errors.Is(err, tt.wantError) {
				t.Errorf("error = %v, want %v", err, tt.wantError)
			}
		})
	}
}

func TestSaveForLoginWithPolicyDoesNotRetryEvaluationFailures(t *testing.T) {
	const secretSentinel = "evaluation-secret-must-not-appear"
	statusFailure := &keychainStatusError{operation: "resolve", status: -50}
	tests := []struct {
		name string
		err  error
	}{
		{name: "cancellation", err: context.Canceled},
		{name: "status failure", err: statusFailure},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evaluations := 0
			operations := 0
			err := saveForLoginWithPolicy(loginSaveOperations{
				evaluate: func() (keychainItemPresence, error) {
					evaluations++
					return keychainItemPresence{}, tt.err
				},
				addPrimary: func() error {
					operations++
					return nil
				},
			})
			if !errors.Is(err, tt.err) {
				t.Errorf("error = %v, want evaluation failure", err)
			}
			if evaluations != 1 || operations != 0 {
				t.Errorf("evaluations/operations = %d/%d, want 1/0", evaluations, operations)
			}
			if strings.Contains(err.Error(), secretSentinel) {
				t.Error("evaluation error disclosed the secret sentinel")
			}
		})
	}
}

func TestNormalizeAllowAnyACL(t *testing.T) {
	const (
		unrelatedPromptFlagA uint16 = 1 << 1
		unrelatedPromptFlagB uint16 = 1 << 15
	)
	tests := []struct {
		name                 string
		count                int
		applicationListIsNil bool
		flags                uint16
		wantFlags            uint16
		wantChanged          bool
		wantCount            int
	}{
		{
			name:                 "one allow-any ACL with normalized flags is unchanged",
			count:                1,
			applicationListIsNil: true,
			flags:                unrelatedPromptFlagA | unrelatedPromptFlagB,
			wantFlags:            unrelatedPromptFlagA | unrelatedPromptFlagB,
		},
		{
			name:                 "one allow-any ACL clears only require-passphrase",
			count:                1,
			applicationListIsNil: true,
			flags:                unrelatedPromptFlagA | keychainPromptRequirePass | unrelatedPromptFlagB,
			wantFlags:            unrelatedPromptFlagA | unrelatedPromptFlagB,
			wantChanged:          true,
		},
		{
			name:        "one creator ACL changes application list to allow-any",
			count:       1,
			flags:       unrelatedPromptFlagA,
			wantFlags:   unrelatedPromptFlagA,
			wantChanged: true,
		},
		{name: "zero decrypt ACLs fail closed", count: 0, wantCount: 0},
		{name: "multiple decrypt ACLs fail closed", count: 2, wantCount: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed, err := normalizeAllowAnyACL(tt.count, tt.applicationListIsNil, tt.flags)
			if tt.count == 1 {
				if err != nil {
					t.Fatalf("normalizeAllowAnyACL() error = %v", err)
				}
				if got != tt.wantFlags {
					t.Errorf("prompt flags = %#x, want %#x", got, tt.wantFlags)
				}
				if changed != tt.wantChanged {
					t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
				}
				return
			}
			var countErr *decryptACLCountError
			if !errors.As(err, &countErr) {
				t.Fatalf("error = %v, want *decryptACLCountError", err)
			}
			if countErr.count != tt.wantCount {
				t.Errorf("decrypt ACL count = %d, want %d", countErr.count, tt.wantCount)
			}
		})
	}
}

func TestWithResolvedKeychainItemUsesOneExactHandle(t *testing.T) {
	identity := 1
	wantHandle := &identity
	resolveCalls := 0
	operationCalls := 0
	releaseCalls := 0

	status := withResolvedKeychainItem(
		func() (*int, func(), int64) {
			resolveCalls++
			return wantHandle, func() { releaseCalls++ }, keychainStatusSuccess
		},
		func(got *int) int64 {
			operationCalls++
			if got != wantHandle {
				t.Error("operation received a different resolved item handle")
			}
			return keychainStatusSuccess
		},
	)
	if status != keychainStatusSuccess {
		t.Errorf("status = %d, want success", status)
	}
	if resolveCalls != 1 || operationCalls != 1 || releaseCalls != 1 {
		t.Errorf("resolve/operation/release calls = %d/%d/%d, want 1/1/1", resolveCalls, operationCalls, releaseCalls)
	}
}

func TestWithResolvedKeychainItemDoesNotOperateOnFailedResolution(t *testing.T) {
	operationCalls := 0
	releaseCalls := 0
	status := withResolvedKeychainItem(
		func() (*int, func(), int64) {
			return nil, func() { releaseCalls++ }, keychainStatusItemNotFound
		},
		func(*int) int64 {
			operationCalls++
			return keychainStatusSuccess
		},
	)
	if status != keychainStatusItemNotFound {
		t.Errorf("status = %d, want item-not-found", status)
	}
	if operationCalls != 0 || releaseCalls != 0 {
		t.Errorf("operation/release calls = %d/%d, want 0/0", operationCalls, releaseCalls)
	}
}

func TestDeleteResolvedKeychainItemMissingIsIdempotent(t *testing.T) {
	tests := []struct {
		name          string
		resolveStatus int64
		deleteStatus  int64
		wantDeletes   int
		wantReleases  int
	}{
		{
			name:          "missing during resolve",
			resolveStatus: keychainStatusItemNotFound,
		},
		{
			name:          "missing during exact-handle delete",
			resolveStatus: keychainStatusSuccess,
			deleteStatus:  keychainStatusItemNotFound,
			wantDeletes:   1,
			wantReleases:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := 1
			wantHandle := &identity
			deleteCalls := 0
			releaseCalls := 0
			err := deleteResolvedKeychainItem(
				func() (*int, func(), int64) {
					return wantHandle, func() { releaseCalls++ }, tt.resolveStatus
				},
				func(got *int) int64 {
					deleteCalls++
					if got != wantHandle {
						t.Error("delete received a different resolved item handle")
					}
					return tt.deleteStatus
				},
			)
			if err != nil {
				t.Errorf("deleteResolvedKeychainItem() error = %v", err)
			}
			if deleteCalls != tt.wantDeletes || releaseCalls != tt.wantReleases {
				t.Errorf("delete/release calls = %d/%d, want %d/%d", deleteCalls, releaseCalls, tt.wantDeletes, tt.wantReleases)
			}
		})
	}
}

func TestDeleteResolvedKeychainItemPreservesStatusWithoutSecrets(t *testing.T) {
	const secretSentinel = "delete-secret-must-not-appear"
	identity := 1
	wantHandle := &identity
	releaseCalls := 0

	err := deleteResolvedKeychainItem(
		func() (*int, func(), int64) {
			return wantHandle, func() { releaseCalls++ }, keychainStatusSuccess
		},
		func(got *int) int64 {
			if got != wantHandle {
				t.Error("delete received a different resolved item handle")
			}
			return -50
		},
	)
	var statusErr *keychainStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %v, want *keychainStatusError", err)
	}
	if statusErr.operation != "delete" || statusErr.status != -50 {
		t.Errorf("status error = %q/%d, want delete/-50", statusErr.operation, statusErr.status)
	}
	if releaseCalls != 1 {
		t.Errorf("release calls = %d, want 1", releaseCalls)
	}
	if strings.Contains(err.Error(), secretSentinel) {
		t.Error("delete status error disclosed the secret sentinel")
	}
}

func recordLoginOperation(calls *[]string, name string, err error) func() error {
	return func() error {
		*calls = append(*calls, name)
		return err
	}
}

func TestTranslateKeychainStatus(t *testing.T) {
	const (
		interactionNotAllowed int64 = -25308
		genericFailure        int64 = -50
	)
	tests := []struct {
		name          string
		status        int64
		wantNotFound  bool
		wantDuplicate bool
		wantStatus    int64
	}{
		{name: "success returns nil", status: keychainStatusSuccess},
		{name: "duplicate maps to logical race", status: keychainStatusDuplicateItem, wantDuplicate: true},
		{name: "item not found maps to logical missing", status: keychainStatusItemNotFound, wantNotFound: true},
		{name: "interaction not allowed remains a typed status error", status: interactionNotAllowed, wantStatus: interactionNotAllowed},
		{name: "generic status remains a typed status error", status: genericFailure, wantStatus: genericFailure},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := translateKeychainStatus("read", tt.status)
			switch {
			case tt.status == keychainStatusSuccess:
				if err != nil {
					t.Errorf("translateKeychainStatus() error = %v", err)
				}
			case tt.wantNotFound:
				if !errors.Is(err, errKeyringNotFound) {
					t.Errorf("error = %v, want errKeyringNotFound", err)
				}
			case tt.wantDuplicate:
				if !errors.Is(err, errKeyringDuplicate) {
					t.Errorf("error = %v, want errKeyringDuplicate", err)
				}
			default:
				var statusErr *keychainStatusError
				if !errors.As(err, &statusErr) {
					t.Fatalf("error = %v, want *keychainStatusError", err)
				}
				if statusErr.operation != "read" || statusErr.status != tt.wantStatus {
					t.Errorf("status error = %q/%d, want read/%d", statusErr.operation, statusErr.status, tt.wantStatus)
				}
			}
		})
	}
}
