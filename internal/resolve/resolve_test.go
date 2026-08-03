package resolve

import (
	"context"
	"errors"
	"testing"

	"github.com/abigotado/trello-cli/internal/errx"
	"github.com/abigotado/trello-cli/internal/trello"
)

// fakeFetcher counts calls so tests can assert that an explicit id costs no
// round trip at all.
type fakeFetcher struct {
	boards []trello.Board
	lists  []trello.List
	err    error
	calls  int
}

func (f *fakeFetcher) Boards(context.Context, bool) ([]trello.Board, error) {
	f.calls++
	return f.boards, f.err
}
func (f *fakeFetcher) Lists(context.Context, string, bool) ([]trello.List, error) {
	f.calls++
	return f.lists, f.err
}
func (f *fakeFetcher) CardsOnBoard(context.Context, string, bool) ([]trello.Card, error) {
	f.calls++
	return nil, f.err
}
func (f *fakeFetcher) Labels(context.Context, string) ([]trello.Label, error) {
	f.calls++
	return nil, f.err
}
func (f *fakeFetcher) BoardMembers(context.Context, string) ([]trello.Member, error) {
	f.calls++
	return nil, f.err
}

func boards() []trello.Board {
	return []trello.Board{
		{ID: "000000000000000000000001", Name: "Roadmap", ShortLink: "aaaaaaaa"},
		{ID: "000000000000000000000002", Name: "Roadmap 2026", ShortLink: "bbbbbbbb"},
		{ID: "000000000000000000000003", Name: "Personal", ShortLink: "cccccccc"},
		{ID: "000000000000000000000004", Name: "personal", ShortLink: "dddddddd"},
	}
}

func TestResolutionLadder(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		fuzzy    bool
		wantID   string
		wantCode errx.Code
	}{
		{"object id resolves directly", "000000000000000000000002", false, "000000000000000000000002", errx.CodeOK},
		// Matched against the real candidate set, not short-circuited on shape.
		{"short link resolves via the index", "cccccccc", false, "000000000000000000000003", errx.CodeOK},
		// Eight alphanumerics is also an ordinary name. Treating the shape as an
		// identifier would make every such board unresolvable by name.
		{"eight-char name is a name, not a short link", "Personal", false, "000000000000000000000003", errx.CodeOK},
		// Exact match must win even though "Roadmap" is also a prefix of
		// "Roadmap 2026"; otherwise an exactly-typed name would be ambiguous.
		{"exact name beats a longer prefix match", "Roadmap", false, "000000000000000000000001", errx.CodeOK},
		{"shared prefix is ambiguous", "Road", false, "", errx.CodeAmbiguous},
		{"prefix matching both cases is ambiguous", "Pers", false, "", errx.CodeAmbiguous},
		{"unique prefix resolves", "Roadmap 2", false, "000000000000000000000002", errx.CodeOK},
		{"no match reports not found", "Nonexistent", false, "", errx.CodeNotFound},
		// Substring only matches when explicitly enabled.
		{"substring is inert without --fuzzy", "2026", false, "", errx.CodeNotFound},
		{"substring resolves with --fuzzy", "2026", true, "000000000000000000000002", errx.CodeOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Resolver{Fetcher: &fakeFetcher{boards: boards()}, Fuzzy: tt.fuzzy}
			got, err := r.Board(context.Background(), tt.query)
			if errx.ExitCode(err) != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (err=%v)", errx.ExitCode(err), tt.wantCode, err)
			}
			if tt.wantCode == errx.CodeOK && got.ID != tt.wantID {
				t.Errorf("id = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

// A 24-hex id must cost no request; that is the whole point of accepting one.
// A shortLink deliberately does not get this treatment, because its shape
// collides with ordinary eight-character names.
func TestExplicitIdentifiersSkipTheFetch(t *testing.T) {
	for _, query := range []string{"000000000000000000000009"} {
		t.Run(query, func(t *testing.T) {
			f := &fakeFetcher{boards: boards()}
			r := &Resolver{Fetcher: f}
			got, err := r.Board(context.Background(), query)
			if err != nil {
				t.Fatalf("Board() error = %v", err)
			}
			if got.ID != query {
				t.Errorf("id = %q, want %q", got.ID, query)
			}
			if f.calls != 0 {
				t.Errorf("fetcher was called %d times; an explicit identifier must cost no request", f.calls)
			}
		})
	}
}

// Ambiguity must hand back everything it matched. Picking one would silently
// act on the wrong board, which is the failure this design exists to prevent.
func TestAmbiguityReturnsCandidatesAndNeverGuesses(t *testing.T) {
	r := &Resolver{Fetcher: &fakeFetcher{boards: boards()}}
	_, err := r.Board(context.Background(), "Road")

	if errx.ExitCode(err) != errx.CodeAmbiguous {
		t.Fatalf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeAmbiguous)
	}
	var typed *errx.Error
	if !errors.As(err, &typed) {
		t.Fatal("expected an *errx.Error")
	}
	if len(typed.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(typed.Candidates))
	}
	for _, c := range typed.Candidates {
		if c.ID == "" || c.Name == "" || c.Kind != "board" {
			t.Errorf("incomplete candidate %+v; the caller cannot disambiguate from it", c)
		}
	}
	if typed.Hint == "" {
		t.Error("ambiguity must state how to disambiguate")
	}
}

// Suggestions are offered even with fuzzy matching off: recovering from a typo
// should cost one more call, and it must not require the tool to have acted on
// a guess.
func TestNotFoundSuggestsNearMissesWithoutFuzzy(t *testing.T) {
	r := &Resolver{Fetcher: &fakeFetcher{boards: boards()}}
	_, err := r.Board(context.Background(), "Rodmap")

	if errx.ExitCode(err) != errx.CodeNotFound {
		t.Fatalf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeNotFound)
	}
	var typed *errx.Error
	if !errors.As(err, &typed) {
		t.Fatal("expected an *errx.Error")
	}
	if len(typed.DidYouMean) == 0 {
		t.Fatal("a one-character typo produced no suggestions")
	}
	if typed.DidYouMean[0].Name != "Roadmap" {
		t.Errorf("closest suggestion = %q, want Roadmap", typed.DidYouMean[0].Name)
	}
}

// A far-off query must not dump the whole board list into the caller's
// context under the guise of being helpful.
func TestNoSuggestionsForAnUnrelatedQuery(t *testing.T) {
	r := &Resolver{Fetcher: &fakeFetcher{boards: boards()}}
	_, err := r.Board(context.Background(), "zzzzzzzzzzzzzzzzz")
	var typed *errx.Error
	if !errors.As(err, &typed) {
		t.Fatal("expected an *errx.Error")
	}
	if len(typed.DidYouMean) != 0 {
		t.Errorf("unrelated query produced %d suggestions", len(typed.DidYouMean))
	}
}

func TestEmptyQueryIsAUsageError(t *testing.T) {
	r := &Resolver{Fetcher: &fakeFetcher{boards: boards()}}
	if _, err := r.Board(context.Background(), "   "); errx.ExitCode(err) != errx.CodeUsage {
		t.Errorf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeUsage)
	}
}

func TestLooksLikeIdentifiers(t *testing.T) {
	tests := []struct {
		value   string
		isID    bool
		isShort bool
	}{
		{"000000000000000000000001", true, false},
		{"AbCdEf0123456789abcdef01", true, false},
		{"aaaaaaaa", false, true},
		{"AbCd0123", false, true},
		// 24 hex chars is an id, so it must not also read as a short link.
		{"aaaaaaaaaaaaaaaaaaaaaaaa", true, false},
		{"Roadmap", false, false},
		{"short", false, false},
		{"aaaaaaaaa", false, false},
		{"zzzzzzzz!", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := LooksLikeID(tt.value); got != tt.isID {
				t.Errorf("LooksLikeID = %v, want %v", got, tt.isID)
			}
			if got := LooksLikeShortLink(tt.value); got != tt.isShort {
				t.Errorf("LooksLikeShortLink = %v, want %v", got, tt.isShort)
			}
		})
	}
}

func TestFetchErrorPropagates(t *testing.T) {
	want := errx.Auth("NOT_AUTHENTICATED", "no credentials")
	r := &Resolver{Fetcher: &fakeFetcher{err: want}}
	if _, err := r.Board(context.Background(), "Roadmap"); errx.ExitCode(err) != errx.CodeAuth {
		t.Errorf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeAuth)
	}
}

func TestListResolutionIsScopedToItsBoard(t *testing.T) {
	f := &fakeFetcher{lists: []trello.List{
		{ID: "000000000000000000000010", Name: "Doing"},
		{ID: "000000000000000000000011", Name: "Done"},
	}}
	r := &Resolver{Fetcher: f}

	got, err := r.List(context.Background(), "board-1", "Doing")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got.ID != "000000000000000000000010" {
		t.Errorf("id = %q", got.ID)
	}
	// "Do" is a prefix of both, so it must report ambiguity rather than pick.
	if _, err := r.List(context.Background(), "board-1", "Do"); errx.ExitCode(err) != errx.CodeAmbiguous {
		t.Errorf("exit code = %d, want %d", errx.ExitCode(err), errx.CodeAmbiguous)
	}
}
