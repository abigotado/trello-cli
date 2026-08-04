package trello

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Field sets are explicit on every call.
//
// Trello returns dozens of fields per object by default, nearly all of them
// irrelevant to a CLI. Asking only for what is rendered is the difference
// between a board listing costing tens of tokens of the caller's context and
// costing thousands.
var (
	boardFields      = []string{"id", "name", "shortLink", "url", "closed", "dateLastActivity"}
	listFields       = []string{"id", "name", "closed", "pos"}
	cardFields       = []string{"id", "name", "shortLink", "idList", "due", "dueComplete", "closed", "url", "idMembers", "labels"}
	labelFields      = []string{"id", "name", "color"}
	memberListFields = []string{"id", "username", "fullName"}
)

// cardFieldList is what a card read asks Trello for, with the description
// appended only when this client was built to want it.
func (c *Client) cardFieldList() string {
	if c.descriptions {
		return strings.Join(append(append([]string{}, cardFields...), "desc"), ",")
	}
	return strings.Join(cardFields, ",")
}

// Board is a Trello board.
type Board struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ShortLink    string `json:"shortLink"`
	URL          string `json:"url,omitempty"`
	Closed       bool   `json:"closed"`
	LastActivity string `json:"dateLastActivity,omitempty"`
}

// List is a column on a board.
type List struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Closed bool    `json:"closed"`
	Pos    float64 `json:"pos,omitempty"`
}

// Label is a board label. Trello allows labels with an empty name, identified
// only by color, which is why color is part of the rendered identity.
type Label struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Card is a Trello card.
type Card struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Desc is fetched but kept out of the default field set; see the OnRequest
	// flag in internal/output. Without it, `cards update --desc` overwrote a
	// description no command could read back.
	Desc        string   `json:"desc,omitempty"`
	ShortLink   string   `json:"shortLink"`
	IDList      string   `json:"idList"`
	Due         string   `json:"due,omitempty"`
	DueComplete bool     `json:"dueComplete"`
	Closed      bool     `json:"closed"`
	URL         string   `json:"url,omitempty"`
	IDMembers   []string `json:"idMembers,omitempty"`
	Labels      []Label  `json:"labels,omitempty"`
}

// Comment is a commentCard action.
type Comment struct {
	ID     string `json:"id"`
	Date   string `json:"date"`
	Text   string `json:"text"`
	Author string `json:"author"`
}

// commentAction is the wire shape Trello returns for a commentCard action.
type commentAction struct {
	ID   string `json:"id"`
	Date string `json:"date"`
	Data struct {
		Text string `json:"text"`
	} `json:"data"`
	MemberCreator struct {
		Username string `json:"username"`
	} `json:"memberCreator"`
}

// Checklist is a card checklist with its items.
type Checklist struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Items []ChecklistItem `json:"checkItems,omitempty"`
}

// ChecklistItem is one entry in a checklist.
type ChecklistItem struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// Attachment is a file or link attached to a card.
type Attachment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Bytes    int64  `json:"bytes,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Date     string `json:"date,omitempty"`
}

// SearchResults holds whatever the search matched.
type SearchResults struct {
	Boards []Board `json:"boards,omitempty"`
	Cards  []Card  `json:"cards,omitempty"`
}

// Boards returns the boards the authenticated member can see.
func (c *Client) Boards(ctx context.Context, includeClosed bool) ([]Board, error) {
	q := url.Values{
		"fields": {strings.Join(boardFields, ",")},
		"filter": {"open"},
	}
	if includeClosed {
		q.Set("filter", "all")
	}
	var boards []Board
	// Nested under /1/members, so exempt from the members route limit and safe
	// to run in parallel with other requests.
	if err := c.Get(ctx, "members/me/boards", q, &boards); err != nil {
		return nil, err
	}
	return boards, nil
}

// Board returns one board by id or shortLink.
func (c *Client) Board(ctx context.Context, id string) (Board, error) {
	var b Board
	q := url.Values{"fields": {strings.Join(boardFields, ",")}}
	if err := c.Get(ctx, "boards/"+url.PathEscape(id), q, &b); err != nil {
		return Board{}, err
	}
	return b, nil
}

// Lists returns the lists on a board.
func (c *Client) Lists(ctx context.Context, boardID string, includeClosed bool) ([]List, error) {
	q := url.Values{
		"fields": {strings.Join(listFields, ",")},
		"filter": {"open"},
	}
	if includeClosed {
		q.Set("filter", "all")
	}
	var lists []List
	if err := c.Get(ctx, "boards/"+url.PathEscape(boardID)+"/lists", q, &lists); err != nil {
		return nil, err
	}
	return lists, nil
}

// Labels returns the labels defined on a board.
func (c *Client) Labels(ctx context.Context, boardID string) ([]Label, error) {
	q := url.Values{
		"fields": {strings.Join(labelFields, ",")},
		"limit":  {"1000"},
	}
	var labels []Label
	if err := c.Get(ctx, "boards/"+url.PathEscape(boardID)+"/labels", q, &labels); err != nil {
		return nil, err
	}
	return labels, nil
}

// BoardMembers returns the members of a board.
func (c *Client) BoardMembers(ctx context.Context, boardID string) ([]Member, error) {
	q := url.Values{"fields": {strings.Join(memberListFields, ",")}}
	var members []Member
	if err := c.Get(ctx, "boards/"+url.PathEscape(boardID)+"/members", q, &members); err != nil {
		return nil, err
	}
	return members, nil
}

// CardsInList returns the cards in a list.
func (c *Client) CardsInList(ctx context.Context, listID string, includeClosed bool) ([]Card, error) {
	return c.cards(ctx, "lists/"+url.PathEscape(listID)+"/cards", includeClosed)
}

// CardsOnBoard returns every card on a board.
func (c *Client) CardsOnBoard(ctx context.Context, boardID string, includeClosed bool) ([]Card, error) {
	return c.cards(ctx, "boards/"+url.PathEscape(boardID)+"/cards", includeClosed)
}

func (c *Client) cards(ctx context.Context, path string, includeClosed bool) ([]Card, error) {
	q := url.Values{
		"fields": {c.cardFieldList()},
		"filter": {"open"},
	}
	if includeClosed {
		q.Set("filter", "all")
	}
	var cards []Card
	if err := c.Get(ctx, path, q, &cards); err != nil {
		return nil, err
	}
	return cards, nil
}

// Card returns one card by id or shortLink.
func (c *Client) Card(ctx context.Context, id string) (Card, error) {
	var card Card
	q := url.Values{"fields": {c.cardFieldList()}}
	if err := c.Get(ctx, "cards/"+url.PathEscape(id), q, &card); err != nil {
		return Card{}, err
	}
	return card, nil
}

// Page bounds a paginated read.
//
// Trello caps actions at 1000 per call and applies its own per-type caps to
// search, so a command that reads either needs a way to say how much it wants
// and where to continue from.
type Page struct {
	// Limit is the maximum number of items to return. Zero means the server
	// default.
	Limit int
	// Before returns only items older than this id or ISO date.
	Before string
	// Since returns only items newer than this id or ISO date.
	Since string
}

func (p Page) apply(q url.Values) {
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Before != "" {
		q.Set("before", p.Before)
	}
	if p.Since != "" {
		q.Set("since", p.Since)
	}
}

// Comments returns a card's comments, newest first.
func (c *Client) Comments(ctx context.Context, cardID string, page Page) ([]Comment, error) {
	q := url.Values{"filter": {"commentCard"}}
	page.apply(q)

	var actions []commentAction
	if err := c.Get(ctx, "cards/"+url.PathEscape(cardID)+"/actions", q, &actions); err != nil {
		return nil, err
	}
	comments := make([]Comment, 0, len(actions))
	for _, a := range actions {
		comments = append(comments, Comment{
			ID:     a.ID,
			Date:   a.Date,
			Text:   a.Data.Text,
			Author: a.MemberCreator.Username,
		})
	}
	return comments, nil
}

// Checklists returns a card's checklists with their items.
func (c *Client) Checklists(ctx context.Context, cardID string) ([]Checklist, error) {
	q := url.Values{
		"fields":          {"id,name"},
		"checkItemFields": {"name,state"},
	}
	var lists []Checklist
	if err := c.Get(ctx, "cards/"+url.PathEscape(cardID)+"/checklists", q, &lists); err != nil {
		return nil, err
	}
	return lists, nil
}

// Attachments returns a card's attachments.
func (c *Client) Attachments(ctx context.Context, cardID string) ([]Attachment, error) {
	q := url.Values{"fields": {"id,name,url,bytes,mimeType,date"}}
	var items []Attachment
	if err := c.Get(ctx, "cards/"+url.PathEscape(cardID)+"/attachments", q, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// Search runs a Trello search.
//
// /1/search is one of the three routes with a dedicated low limit, so the
// client serializes it rather than letting it run alongside other requests.
func (c *Client) Search(ctx context.Context, query string, limit int) (SearchResults, error) {
	if strings.TrimSpace(query) == "" {
		return SearchResults{}, nil
	}
	q := url.Values{
		"query":        {query},
		"modelTypes":   {"boards,cards"},
		"board_fields": {strings.Join(boardFields, ",")},
		"card_fields":  {c.cardFieldList()},
	}
	if limit > 0 {
		q.Set("boards_limit", strconv.Itoa(limit))
		q.Set("cards_limit", strconv.Itoa(limit))
	}
	var results SearchResults
	if err := c.Get(ctx, "search", q, &results); err != nil {
		return SearchResults{}, err
	}
	return results, nil
}

// ParseTime parses a Trello timestamp, returning the zero time when absent or
// malformed. Callers render dates; none of them should fail on a bad one.
func ParseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
