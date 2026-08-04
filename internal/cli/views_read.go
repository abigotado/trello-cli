package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/abigotado/trello-cli/internal/output"
	"github.com/abigotado/trello-cli/internal/trello"
)

// Views adapt Trello models to output.Renderable.
//
// They live in internal/cli because internal/trello must not format
// user-facing text and must not depend on internal/output.
//
// Every Field carries both a Value for the compact text renderer and a Raw for
// --fields projection, so a boolean stays a boolean in JSON rather than
// becoming the string a human reads.

type boardView struct{ trello.Board }

func (b boardView) Fields() []output.Field {
	return []output.Field{
		{Name: "id", Value: b.ID, Raw: b.ID},
		{Name: "shortLink", Value: b.ShortLink, Raw: b.ShortLink},
		{Name: "name", Value: b.Name, Raw: b.Name},
		{Name: "closed", Value: flag(b.Closed, "closed"), Raw: b.Closed},
		{Name: "url", Value: b.URL, Raw: b.URL},
		{Name: "lastActivity", Value: b.LastActivity, Raw: b.LastActivity},
	}
}

type listView struct{ trello.List }

func (l listView) Fields() []output.Field {
	return []output.Field{
		{Name: "id", Value: l.ID, Raw: l.ID},
		{Name: "name", Value: l.Name, Raw: l.Name},
		{Name: "closed", Value: flag(l.Closed, "closed"), Raw: l.Closed},
		{Name: "pos", Value: "", Raw: l.Pos},
	}
}

// cardView renders a card. listName is filled in when the command knows it, so
// a card listing reads without a second lookup.
type cardView struct {
	trello.Card
	listName string
}

func (c cardView) Fields() []output.Field {
	labels := make([]string, 0, len(c.Labels))
	for _, l := range c.Labels {
		name := l.Name
		if name == "" {
			name = l.Color
		}
		labels = append(labels, name)
	}
	due := c.Due
	if due != "" && c.DueComplete {
		due += " (done)"
	}
	return []output.Field{
		{Name: "id", Value: c.ID, Raw: c.ID},
		{Name: "shortLink", Value: c.ShortLink, Raw: c.ShortLink},
		{Name: "name", Value: c.Name, Raw: c.Name},
		// OnRequest: a description can run to paragraphs, and putting one in
		// every row of a large listing would spend the caller's context on
		// something it did not ask for. Ask for it with --fields desc.
		{Name: "desc", Value: c.Desc, Raw: c.Desc, OnRequest: true},
		{Name: "list", Value: c.listName, Raw: c.listName},
		{Name: "due", Value: due, Raw: c.Due},
		{Name: "dueComplete", Value: "", Raw: c.DueComplete},
		{Name: "labels", Value: strings.Join(labels, ","), Raw: labels},
		{Name: "closed", Value: flag(c.Closed, "closed"), Raw: c.Closed},
		{Name: "url", Value: c.URL, Raw: c.URL},
		{Name: "members", Value: "", Raw: c.IDMembers},
	}
}

type labelView struct{ trello.Label }

func (l labelView) Fields() []output.Field {
	return []output.Field{
		{Name: "id", Value: l.ID, Raw: l.ID},
		{Name: "color", Value: l.Color, Raw: l.Color},
		{Name: "name", Value: l.Name, Raw: l.Name},
	}
}

type memberListView struct{ trello.Member }

func (m memberListView) Fields() []output.Field {
	return []output.Field{
		{Name: "id", Value: m.ID, Raw: m.ID},
		{Name: "username", Value: "@" + m.Username, Raw: m.Username},
		{Name: "fullName", Value: m.FullName, Raw: m.FullName},
	}
}

type commentView struct{ trello.Comment }

func (c commentView) Fields() []output.Field {
	return []output.Field{
		{Name: "id", Value: c.ID, Raw: c.ID},
		{Name: "date", Value: c.Date, Raw: c.Date},
		{Name: "author", Value: "@" + c.Author, Raw: c.Author},
		// Newlines would break the one-line-per-entity contract of text output.
		{Name: "text", Value: oneLine(c.Text), Raw: c.Text},
	}
}

type checklistView struct{ trello.Checklist }

func (c checklistView) Fields() []output.Field {
	done := 0
	names := make([]string, 0, len(c.Items))
	for _, item := range c.Items {
		mark := " "
		if item.State == "complete" {
			mark = "x"
			done++
		}
		names = append(names, "["+mark+"] "+item.Name)
	}
	return []output.Field{
		{Name: "id", Value: c.ID, Raw: c.ID},
		{Name: "name", Value: c.Name, Raw: c.Name},
		{Name: "progress", Value: fmt.Sprintf("%d/%d", done, len(c.Items)), Raw: map[string]int{"done": done, "total": len(c.Items)}},
		{Name: "items", Value: strings.Join(names, "; "), Raw: c.Items},
	}
}

type attachmentView struct{ trello.Attachment }

func (a attachmentView) Fields() []output.Field {
	size := ""
	if a.Bytes > 0 {
		size = strconv.FormatInt(a.Bytes, 10) + "B"
	}
	return []output.Field{
		{Name: "id", Value: a.ID, Raw: a.ID},
		{Name: "name", Value: a.Name, Raw: a.Name},
		{Name: "url", Value: a.URL, Raw: a.URL},
		{Name: "bytes", Value: size, Raw: a.Bytes},
		{Name: "mimeType", Value: a.MimeType, Raw: a.MimeType},
		{Name: "date", Value: a.Date, Raw: a.Date},
	}
}

// searchHitView renders a board or card match under one shape, so a search
// result set is a single homogeneous list rather than two the caller must
// branch on.
type searchHitView struct {
	Kind      string
	ID        string
	ShortLink string
	Name      string
	URL       string
}

func (s searchHitView) Fields() []output.Field {
	return []output.Field{
		{Name: "kind", Value: s.Kind, Raw: s.Kind},
		{Name: "id", Value: s.ID, Raw: s.ID},
		{Name: "shortLink", Value: s.ShortLink, Raw: s.ShortLink},
		{Name: "name", Value: s.Name, Raw: s.Name},
		{Name: "url", Value: s.URL, Raw: s.URL},
	}
}

// flag renders a boolean as a label or nothing, so text output stays compact
// and the common case adds no noise.
func flag(b bool, label string) string {
	if b {
		return label
	}
	return ""
}

// oneLine collapses whitespace so multi-line content cannot break the
// one-entity-per-line contract of text output.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
