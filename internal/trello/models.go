package trello

import (
	"context"
	"net/url"
)

// Member is a Trello user.
//
// The struct carries no display logic: rendering lives in internal/output, and
// this package must not format user-facing text.
type Member struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	FullName string `json:"fullName"`
	Email    string `json:"email,omitempty"`
	URL      string `json:"url,omitempty"`
	Initials string `json:"initials,omitempty"`
}

// memberFields is the default projection for a member.
//
// Trello returns roughly forty fields for a member, nearly all of them
// irrelevant here. Asking for the handful actually rendered keeps a routine
// call from spending thousands of tokens of the caller's context.
var memberFields = []string{"id", "username", "fullName", "email", "url", "initials"}

// Me returns the authenticated member.
func (c *Client) Me(ctx context.Context) (Member, error) {
	var m Member
	q := url.Values{"fields": {joinFields(memberFields)}}
	if err := c.Get(ctx, "members/me", q, &m); err != nil {
		return Member{}, err
	}
	return m, nil
}

func joinFields(fields []string) string {
	out := ""
	for i, f := range fields {
		if i > 0 {
			out += ","
		}
		out += f
	}
	return out
}
