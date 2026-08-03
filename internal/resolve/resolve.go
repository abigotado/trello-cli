// Package resolve turns the names a caller actually has into Trello ids.
//
// An agent almost never holds a 24-character object id; it holds "Sprint 12"
// and "Doing". Without resolution every task costs two or three extra round
// trips just to look them up, which is the single largest avoidable cost in
// driving this tool.
//
// The one rule that matters here: resolution never guesses. Ambiguity returns
// candidates and a distinct exit code so the caller can disambiguate in one
// more call, and a near miss returns suggestions rather than acting on the
// closest thing it found.
package resolve

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/abigotado/trello-cli/internal/errx"
	"github.com/abigotado/trello-cli/internal/trello"
)

// Kind names what is being resolved. It becomes part of the error code, so the
// values are stable.
type Kind string

// Resolvable kinds.
const (
	KindBoard  Kind = "board"
	KindList   Kind = "list"
	KindCard   Kind = "card"
	KindLabel  Kind = "label"
	KindMember Kind = "member"
)

// Object is a resolved Trello object.
type Object struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ShortLink string `json:"short_link,omitempty"`
}

// idPattern matches a Trello object id: 24 hex characters.
var idPattern = regexp.MustCompile(`^[0-9a-fA-F]{24}$`)

// shortLinkPattern matches a Trello shortLink: 8 alphanumerics.
var shortLinkPattern = regexp.MustCompile(`^[0-9a-zA-Z]{8}$`)

// LooksLikeID reports whether query is already an object id.
//
// Precedence is deliberate and documented: a query in id form is treated as an
// id, even in the pathological case of an object literally named like one.
// Anything else would make an explicit id ambiguous, which defeats the point
// of having --board-id as an escape hatch.
func LooksLikeID(query string) bool { return idPattern.MatchString(query) }

// LooksLikeShortLink reports whether query has the shape of a Trello
// shortLink: eight alphanumerics.
//
// This shape is NOT sufficient to treat a query as an identifier. Real board
// and list names are routinely eight characters — "Personal", "Backlog1",
// "Roadmap2" — and short-circuiting on the shape alone would make every one of
// them unresolvable by name. Use it only where the caller has already stated
// the value is an id, such as behind --board-id.
func LooksLikeShortLink(query string) bool {
	return shortLinkPattern.MatchString(query) && !LooksLikeID(query)
}

// Fetcher supplies the candidate sets. The Trello client satisfies it; tests
// substitute a fake so resolution is tested without a server.
type Fetcher interface {
	Boards(ctx context.Context, includeClosed bool) ([]trello.Board, error)
	Lists(ctx context.Context, boardID string, includeClosed bool) ([]trello.List, error)
	CardsOnBoard(ctx context.Context, boardID string, includeClosed bool) ([]trello.Card, error)
	Labels(ctx context.Context, boardID string) ([]trello.Label, error)
	BoardMembers(ctx context.Context, boardID string) ([]trello.Member, error)
}

// Resolver resolves names to ids, backed by an on-disk index.
type Resolver struct {
	Fetcher Fetcher
	Cache   *Cache
	// Strict stops the ladder at an exact name.
	//
	// Destructive commands set it: saving one round trip is not worth deleting
	// the wrong card because a prefix happened to be unique at that moment.
	Strict bool
	// Fuzzy enables the last matching rung. Off by default: fuzzy matching
	// fails by confidently returning a single wrong answer, not by reporting
	// ambiguity, so a hallucinated name would be silently coerced onto a real
	// object and then acted upon.
	Fuzzy bool
	// Now is injected by tests.
	Now func() time.Time
}

func (r *Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Board resolves a board reference.
func (r *Resolver) Board(ctx context.Context, query string) (Object, error) {
	return r.resolve(ctx, KindBoard, "boards", query, func(ctx context.Context) ([]Object, error) {
		boards, err := r.Fetcher.Boards(ctx, false)
		if err != nil {
			return nil, err
		}
		out := make([]Object, 0, len(boards))
		for _, b := range boards {
			out = append(out, Object{ID: b.ID, Name: b.Name, ShortLink: b.ShortLink})
		}
		return out, nil
	})
}

// List resolves a list reference within a board.
func (r *Resolver) List(ctx context.Context, boardID, query string) (Object, error) {
	return r.resolve(ctx, KindList, "lists:"+boardID, query, func(ctx context.Context) ([]Object, error) {
		lists, err := r.Fetcher.Lists(ctx, boardID, false)
		if err != nil {
			return nil, err
		}
		out := make([]Object, 0, len(lists))
		for _, l := range lists {
			out = append(out, Object{ID: l.ID, Name: l.Name})
		}
		return out, nil
	})
}

// Card resolves a card reference within a board.
func (r *Resolver) Card(ctx context.Context, boardID, query string) (Object, error) {
	return r.resolve(ctx, KindCard, "cards:"+boardID, query, func(ctx context.Context) ([]Object, error) {
		cards, err := r.Fetcher.CardsOnBoard(ctx, boardID, false)
		if err != nil {
			return nil, err
		}
		out := make([]Object, 0, len(cards))
		for _, c := range cards {
			out = append(out, Object{ID: c.ID, Name: c.Name, ShortLink: c.ShortLink})
		}
		return out, nil
	})
}

// Label resolves a label reference within a board.
//
// Trello permits labels with an empty name, so a color-only label is addressed
// by its color.
func (r *Resolver) Label(ctx context.Context, boardID, query string) (Object, error) {
	return r.resolve(ctx, KindLabel, "labels:"+boardID, query, func(ctx context.Context) ([]Object, error) {
		labels, err := r.Fetcher.Labels(ctx, boardID)
		if err != nil {
			return nil, err
		}
		out := make([]Object, 0, len(labels))
		for _, l := range labels {
			name := l.Name
			if name == "" {
				name = l.Color
			}
			out = append(out, Object{ID: l.ID, Name: name})
		}
		return out, nil
	})
}

// Member resolves a member reference within a board, by username or full name.
func (r *Resolver) Member(ctx context.Context, boardID, query string) (Object, error) {
	return r.resolve(ctx, KindMember, "members:"+boardID, query, func(ctx context.Context) ([]Object, error) {
		members, err := r.Fetcher.BoardMembers(ctx, boardID)
		if err != nil {
			return nil, err
		}
		out := make([]Object, 0, len(members))
		for _, m := range members {
			out = append(out, Object{ID: m.ID, Name: m.Username, ShortLink: m.FullName})
		}
		return out, nil
	})
}

// Flush persists any index changes this invocation made. Call it once, at the
// end; nothing is written before it.
func (r *Resolver) Flush() {
	if r.Cache != nil {
		r.Cache.Flush()
	}
}

// Invalidate drops a cached scope, so the next resolution goes live.
func (r *Resolver) Invalidate(scope string) {
	if r.Cache != nil {
		r.Cache.Invalidate(scope)
	}
}

// BoardScope and friends name the cache scopes, so callers can invalidate the
// exact one a stale id came from.
func BoardScope() string              { return "boards" }
func ListScope(boardID string) string { return "lists:" + boardID }
func CardScope(boardID string) string { return "cards:" + boardID }

// resolve runs the ladder for one kind.
func (r *Resolver) resolve(
	ctx context.Context,
	kind Kind,
	scope, query string,
	fetch func(context.Context) ([]Object, error),
) (Object, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Object{}, errx.Usage("a %s name or id is required", kind)
	}
	// A 24-hex id needs no index at all: nothing is realistically named that,
	// and this is the round trip the --*-id escape hatches exist to save.
	//
	// ShortLinks deliberately do NOT short-circuit here. They are eight
	// alphanumerics, a shape that collides with ordinary names like "Personal",
	// and treating those as ids would make them permanently unresolvable. A
	// shortLink still matches at the first rung below, against the real
	// candidate set, and --board-id passes one through with no lookup.
	if LooksLikeID(query) {
		return Object{ID: query}, nil
	}

	if cached, ok := r.cacheGet(scope); ok {
		if obj, err := match(kind, query, cached, r.Fuzzy, r.Strict); err == nil {
			return obj, nil
		}
		// A cache miss is never authoritative. Falling through to a live
		// lookup is what makes creating a list and immediately addressing it
		// by name work.
	}

	objects, err := fetch(ctx)
	if err != nil {
		return Object{}, err
	}
	r.cachePut(scope, objects)
	return match(kind, query, objects, r.Fuzzy, r.Strict)
}

func (r *Resolver) cacheGet(scope string) ([]Object, bool) {
	if r.Cache == nil {
		return nil, false
	}
	return r.Cache.Get(scope)
}

func (r *Resolver) cachePut(scope string, objects []Object) {
	if r.Cache == nil {
		return
	}
	r.Cache.Put(scope, objects, r.now())
}

// match runs the resolution ladder against a candidate set.
func match(kind Kind, query string, objects []Object, fuzzy, strict bool) (Object, error) {
	// Rung 1: an id or shortLink present in the set.
	for _, o := range objects {
		if o.ID == query || (o.ShortLink != "" && o.ShortLink == query) {
			return o, nil
		}
	}

	lower := strings.ToLower(query)

	// Rung 2: exact name, case sensitive first so an exactly-typed name always
	// wins over a case-insensitive collision.
	if hits := filter(objects, func(o Object) bool { return o.Name == query }); len(hits) == 1 {
		return hits[0], nil
	} else if len(hits) > 1 {
		return Object{}, ambiguous(kind, query, hits)
	}

	// Rung 3: exact name, case insensitive.
	hits := filter(objects, func(o Object) bool { return strings.ToLower(o.Name) == lower })
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		return Object{}, ambiguous(kind, query, hits)
	}

	// Rungs below here narrow by something less than an exact name, so a
	// destructive caller stops at this point and reports what it could not
	// match exactly.
	if strict {
		return Object{}, errx.NotFound(string(kind), query, suggestions(kind, query, objects))
	}

	// Rung 4: unique case-insensitive prefix.
	hits = filter(objects, func(o Object) bool { return strings.HasPrefix(strings.ToLower(o.Name), lower) })
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		return Object{}, ambiguous(kind, query, hits)
	}

	// Rung 5: substring, only when explicitly enabled.
	if fuzzy {
		hits = filter(objects, func(o Object) bool { return strings.Contains(strings.ToLower(o.Name), lower) })
		if len(hits) == 1 {
			return hits[0], nil
		}
		if len(hits) > 1 {
			return Object{}, ambiguous(kind, query, hits)
		}
	}

	return Object{}, errx.NotFound(string(kind), query, suggestions(kind, query, objects))
}

func filter(objects []Object, keep func(Object) bool) []Object {
	var out []Object
	for _, o := range objects {
		if keep(o) {
			out = append(out, o)
		}
	}
	return out
}

func ambiguous(kind Kind, query string, hits []Object) error {
	return errx.Ambiguous(string(kind), query, candidates(kind, hits))
}

func candidates(kind Kind, objects []Object) []errx.Candidate {
	out := make([]errx.Candidate, 0, len(objects))
	for _, o := range objects {
		out = append(out, errx.Candidate{ID: o.ID, Name: o.Name, Kind: string(kind)})
	}
	return out
}

// maxSuggestions bounds did_you_mean. The point is a cheap second call, not a
// directory listing pasted into the caller's context.
const maxSuggestions = 5

// suggestions returns the nearest names to a query that matched nothing.
//
// These are computed even when fuzzy matching is disabled: recovering from a
// typo should cost one more call, and it does not require the tool to have
// acted on a guess.
func suggestions(kind Kind, query string, objects []Object) []errx.Candidate {
	lower := strings.ToLower(query)
	type scored struct {
		obj   Object
		score int
	}
	var ranked []scored
	for _, o := range objects {
		d := editDistance(lower, strings.ToLower(o.Name))
		// Only offer something genuinely close. Suggesting every board is
		// noise the caller has to pay for in context.
		if d <= max(2, len(lower)/2) {
			ranked = append(ranked, scored{o, d})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score < ranked[j].score })
	if len(ranked) > maxSuggestions {
		ranked = ranked[:maxSuggestions]
	}
	out := make([]errx.Candidate, 0, len(ranked))
	for _, s := range ranked {
		out = append(out, errx.Candidate{ID: s.obj.ID, Name: s.obj.Name, Kind: string(kind)})
	}
	return out
}

// editDistance is Levenshtein distance over runes, with a rolling row.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}
