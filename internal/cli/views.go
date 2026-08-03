package cli

import (
	"github.com/abigotado-niko/trello-cli/internal/auth"
	"github.com/abigotado-niko/trello-cli/internal/output"
	"github.com/abigotado-niko/trello-cli/internal/trello"
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

// authStatusView renders credential status.
//
// It carries a fingerprint, never the token: printing a credential is the one
// mistake in this tool that cannot be walked back.
type authStatusView struct {
	Authenticated bool        `json:"authenticated"`
	Source        auth.Source `json:"source"`
	APIKeySuffix  string      `json:"api_key_suffix,omitempty"`
	Fingerprint   string      `json:"token_fingerprint,omitempty"`
}

func (s authStatusView) Fields() []output.Field {
	state := "not authenticated"
	if s.Authenticated {
		state = "authenticated"
	}
	return []output.Field{
		{Name: "authenticated", Value: state, Raw: s.Authenticated},
		{Name: "source", Value: string(s.Source), Raw: string(s.Source)},
		{Name: "api_key_suffix", Value: s.APIKeySuffix, Raw: s.APIKeySuffix},
		{Name: "token_fingerprint", Value: s.Fingerprint, Raw: s.Fingerprint},
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
