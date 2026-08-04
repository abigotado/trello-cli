package cli

import (
	"github.com/abigotado/trello-cli/internal/auth"
	"github.com/abigotado/trello-cli/internal/output"
	"github.com/abigotado/trello-cli/internal/trello"
)

// Views adapt domain types to output.Renderable.
//
// They live here rather than in internal/trello because that package must not
// format user-facing text, and rendering must not force the client to depend on
// internal/output.

// memberView renders a Trello member.
type memberView struct{ trello.Member }

func (m memberView) Fields() []output.Field {
	return []output.Field{
		{Name: "id", Value: m.ID, Raw: m.ID},
		{Name: "username", Value: "@" + m.Username, Raw: m.Username},
		{Name: "fullName", Value: m.FullName, Raw: m.FullName},
		{Name: "email", Value: m.Email, Raw: m.Email},
		{Name: "url", Value: m.URL, Raw: m.URL},
	}
}

// accountView renders one account's credential status.
//
// It carries a fingerprint, never the token: printing a credential is the one
// mistake in this tool that cannot be walked back.
type accountView struct {
	Account string `json:"account,omitempty"`
	// Credential says how much this command actually established, which is not
	// the same question for every command. Reporting a bool alone made a
	// command that never looked in the keychain print "not authenticated" for
	// an account that was perfectly usable — `auth list` and `auth default`
	// both did, contradicting `auth status` about the same account in the same
	// session.
	Credential   Credential  `json:"credential"`
	Source       auth.Source `json:"source"`
	APIKeySuffix string      `json:"apiKeySuffix,omitempty"`
	Fingerprint  string      `json:"tokenFingerprint,omitempty"`
	Default      bool        `json:"default"`
}

// Credential is how far a command got in establishing that an account is
// usable.
type Credential string

const (
	// CredentialNone is "this account has nothing stored", which is a finding,
	// not an absence of one.
	CredentialNone Credential = "none"
	// CredentialStored is what the registry alone can tell you: a credential
	// was saved under this name and not logged out. Reaching this needs no
	// keychain access, which is why the listing path stops here — on macOS an
	// unsigned binary raises a modal prompt per account, and an agent would
	// hang on the first invisible dialog.
	CredentialStored Credential = "stored"
	// CredentialPresent means the keychain was actually read and yielded a
	// well-formed key and token. It is still not proof Trello will accept
	// them; nothing short of a request is.
	CredentialPresent Credential = "present"
)

func (s accountView) Fields() []output.Field {
	state := string(s.Credential)
	if state == "" {
		state = string(CredentialNone)
	}
	marker := ""
	if s.Default {
		marker = "(default)"
	}
	return []output.Field{
		{Name: "account", Value: s.Account, Raw: s.Account},
		{Name: "default", Value: marker, Raw: s.Default},
		{Name: "credential", Value: state, Raw: state},
		{Name: "source", Value: string(s.Source), Raw: string(s.Source)},
		{Name: "apiKeySuffix", Value: s.APIKeySuffix, Raw: s.APIKeySuffix},
		{Name: "tokenFingerprint", Value: s.Fingerprint, Raw: s.Fingerprint},
	}
}

// keySuffix returns at most the last four characters of an API key, so status
// output can distinguish two accounts without disclosing either key.
func keySuffix(key string) string {
	if len(key) <= 4 {
		return ""
	}
	return "..." + key[len(key)-4:]
}
