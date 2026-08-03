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
	Account       string      `json:"account,omitempty"`
	Authenticated bool        `json:"authenticated"`
	Source        auth.Source `json:"source"`
	APIKeySuffix  string      `json:"apiKeySuffix,omitempty"`
	Fingerprint   string      `json:"tokenFingerprint,omitempty"`
	Default       bool        `json:"default"`
}

func (s accountView) Fields() []output.Field {
	state := "not authenticated"
	if s.Authenticated {
		state = "authenticated"
	}
	marker := ""
	if s.Default {
		marker = "(default)"
	}
	return []output.Field{
		{Name: "account", Value: s.Account, Raw: s.Account},
		{Name: "default", Value: marker, Raw: s.Default},
		{Name: "authenticated", Value: state, Raw: s.Authenticated},
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
