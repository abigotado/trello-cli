package trello

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// Trello takes write parameters in the query string rather than a JSON body,
// so each method below builds url.Values and lets the shared transport apply
// pacing, retry classification, and credential redaction.

// CreateList adds a list to a board. pos accepts "top", "bottom", or a number.
func (c *Client) CreateList(ctx context.Context, boardID, name, pos string) (List, error) {
	q := url.Values{"idBoard": {boardID}, "name": {name}}
	if pos != "" {
		q.Set("pos", pos)
	}
	var out List
	if err := c.Post(ctx, "lists", q, &out); err != nil {
		return List{}, err
	}
	return out, nil
}

// SetListClosed archives or restores a list.
//
// Trello has no delete for lists; archiving is the strongest operation, which
// is why nothing here is marked destructive.
func (c *Client) SetListClosed(ctx context.Context, listID string, closed bool) (List, error) {
	q := url.Values{"value": {strconv.FormatBool(closed)}}
	var out List
	if err := c.Put(ctx, "lists/"+url.PathEscape(listID)+"/closed", q, &out); err != nil {
		return List{}, err
	}
	return out, nil
}

// CardInput is the mutable surface of a card.
//
// Pointer fields distinguish "not supplied" from "set to empty", so clearing a
// due date is expressible and an unmentioned field is never overwritten.
type CardInput struct {
	Name        *string
	Desc        *string
	Due         *string
	DueComplete *bool
	Pos         *string
	IDList      *string
	Closed      *bool
}

func (in CardInput) apply(q url.Values) {
	if in.Name != nil {
		q.Set("name", *in.Name)
	}
	if in.Desc != nil {
		q.Set("desc", *in.Desc)
	}
	if in.Due != nil {
		// An empty string is how Trello clears a due date, which is why this
		// is set unconditionally once the caller supplied the field.
		q.Set("due", *in.Due)
	}
	if in.DueComplete != nil {
		q.Set("dueComplete", strconv.FormatBool(*in.DueComplete))
	}
	if in.Pos != nil {
		q.Set("pos", *in.Pos)
	}
	if in.IDList != nil {
		q.Set("idList", *in.IDList)
	}
	if in.Closed != nil {
		q.Set("closed", strconv.FormatBool(*in.Closed))
	}
}

// CreateCard adds a card to a list.
func (c *Client) CreateCard(ctx context.Context, listID string, in CardInput) (Card, error) {
	q := url.Values{"idList": {listID}}
	in.apply(q)
	q.Set("fields", strings.Join(cardFields, ","))
	var out Card
	if err := c.Post(ctx, "cards", q, &out); err != nil {
		return Card{}, err
	}
	return out, nil
}

// UpdateCard changes the supplied fields of a card and leaves the rest alone.
func (c *Client) UpdateCard(ctx context.Context, cardID string, in CardInput) (Card, error) {
	q := url.Values{}
	in.apply(q)
	q.Set("fields", strings.Join(cardFields, ","))
	var out Card
	if err := c.Put(ctx, "cards/"+url.PathEscape(cardID), q, &out); err != nil {
		return Card{}, err
	}
	return out, nil
}

// DeleteCard permanently removes a card. Trello offers no undo for this.
func (c *Client) DeleteCard(ctx context.Context, cardID string) error {
	return c.Delete(ctx, "cards/"+url.PathEscape(cardID), nil)
}

// CreateLabel defines a new label on a board.
//
// This is a different operation from AddLabel, which attaches a label that
// already exists to a card. A board's label set is the vocabulary; a card
// carries some of it.
//
// The colour is passed through rather than checked against a list here. Trello
// owns that enum and has extended it — the light and dark variants did not
// always exist — so a local allowlist would reject colours the API accepts. A
// rejected colour comes back as a usage error naming what was sent.
func (c *Client) CreateLabel(ctx context.Context, boardID, name, color string) (Label, error) {
	q := url.Values{"name": {name}, "color": {color}}
	var out Label
	if err := c.Post(ctx, "boards/"+url.PathEscape(boardID)+"/labels", q, &out); err != nil {
		return Label{}, err
	}
	return out, nil
}

// DeleteLabel removes a label from the board entirely, and with it from every
// card that carried it. Trello offers no undo.
func (c *Client) DeleteLabel(ctx context.Context, labelID string) error {
	return c.Delete(ctx, "labels/"+url.PathEscape(labelID), nil)
}

// AddLabel attaches a board label to a card.
func (c *Client) AddLabel(ctx context.Context, cardID, labelID string) error {
	q := url.Values{"value": {labelID}}
	return c.Post(ctx, "cards/"+url.PathEscape(cardID)+"/idLabels", q, nil)
}

// RemoveLabel detaches a label from a card.
func (c *Client) RemoveLabel(ctx context.Context, cardID, labelID string) error {
	return c.Delete(ctx, "cards/"+url.PathEscape(cardID)+"/idLabels/"+url.PathEscape(labelID), nil)
}

// AssignMember adds a member to a card.
func (c *Client) AssignMember(ctx context.Context, cardID, memberID string) error {
	q := url.Values{"value": {memberID}}
	return c.Post(ctx, "cards/"+url.PathEscape(cardID)+"/idMembers", q, nil)
}

// UnassignMember removes a member from a card.
func (c *Client) UnassignMember(ctx context.Context, cardID, memberID string) error {
	return c.Delete(ctx, "cards/"+url.PathEscape(cardID)+"/idMembers/"+url.PathEscape(memberID), nil)
}

// AddComment posts a comment to a card.
func (c *Client) AddComment(ctx context.Context, cardID, text string) (Comment, error) {
	q := url.Values{"text": {text}}
	var action commentAction
	if err := c.Post(ctx, "cards/"+url.PathEscape(cardID)+"/actions/comments", q, &action); err != nil {
		return Comment{}, err
	}
	return Comment{
		ID:     action.ID,
		Date:   action.Date,
		Text:   action.Data.Text,
		Author: action.MemberCreator.Username,
	}, nil
}

// CreateChecklist adds a checklist to a card.
func (c *Client) CreateChecklist(ctx context.Context, cardID, name string) (Checklist, error) {
	q := url.Values{"idCard": {cardID}, "name": {name}}
	var out Checklist
	if err := c.Post(ctx, "checklists", q, &out); err != nil {
		return Checklist{}, err
	}
	return out, nil
}

// AddCheckItem adds an item to a checklist.
func (c *Client) AddCheckItem(ctx context.Context, checklistID, name string, checked bool) (ChecklistItem, error) {
	q := url.Values{"name": {name}, "checked": {strconv.FormatBool(checked)}}
	var out ChecklistItem
	if err := c.Post(ctx, "checklists/"+url.PathEscape(checklistID)+"/checkItems", q, &out); err != nil {
		return ChecklistItem{}, err
	}
	return out, nil
}

// SetCheckItemState marks a check item complete or incomplete.
//
// The update runs against the card rather than the checklist: that is the only
// route Trello exposes for changing an item's state.
func (c *Client) SetCheckItemState(ctx context.Context, cardID, itemID string, complete bool) (ChecklistItem, error) {
	state := "incomplete"
	if complete {
		state = "complete"
	}
	q := url.Values{"state": {state}}
	var out ChecklistItem
	path := "cards/" + url.PathEscape(cardID) + "/checkItem/" + url.PathEscape(itemID)
	if err := c.Put(ctx, path, q, &out); err != nil {
		return ChecklistItem{}, err
	}
	return out, nil
}

// AddAttachment attaches a URL to a card.
//
// Only URL attachments are supported: uploading a file needs multipart, which
// the query-parameter transport here does not do, and pretending otherwise
// would fail confusingly at runtime.
func (c *Client) AddAttachment(ctx context.Context, cardID, attachURL, name string) (Attachment, error) {
	q := url.Values{"url": {attachURL}}
	if name != "" {
		q.Set("name", name)
	}
	var out Attachment
	if err := c.Post(ctx, "cards/"+url.PathEscape(cardID)+"/attachments", q, &out); err != nil {
		return Attachment{}, err
	}
	return out, nil
}
