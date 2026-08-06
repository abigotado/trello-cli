package cli

import (
	"context"
	"errors"

	"github.com/abigotado/trello-cli/internal/errx"
	"github.com/abigotado/trello-cli/internal/resolve"
	"github.com/spf13/cobra"
)

// objectRef is the name-or-id pair a command accepts for one object.
//
// Both spellings exist on purpose: name is what an agent has, id is the escape
// hatch for when a name is ambiguous or when the caller must be certain.
type objectRef struct {
	name string
	id   string
}

func (r objectRef) empty() bool { return r.name == "" && r.id == "" }

// nameOrID returns whichever spelling the caller supplied, preferring the
// explicit id.
func (r objectRef) nameOrID() string {
	if r.id != "" {
		return r.id
	}
	return r.name
}

// bind registers --<kind> and --<kind>-id on cmd.
func (r *objectRef) bind(cmd *cobra.Command, kind, help string) {
	cmd.Flags().StringVar(&r.name, kind, "", help)
	cmd.Flags().StringVar(&r.id, kind+"-id", "", "exact "+kind+" id, skipping name resolution")
}

// needsLookup reports whether ref can only be resolved by consulting the index.
//
// An id or an explicit --*-id needs nothing, which is what lets a command that
// was given every object by id skip resolving a board it never uses.
func (r objectRef) needsLookup() bool {
	if r.id != "" {
		return false
	}
	return r.name != "" && !resolve.LooksLikeID(r.name)
}

// boardFor resolves the board only when one of refs actually requires an index
// lookup scoped to it.
//
// Without this, `labels add --card-id X --label-id Y` fails with "a board is
// required" even though nothing in that call needs one — every object was
// already addressed unambiguously.
func (a *App) boardFor(ctx context.Context, boardRef objectRef, refs ...objectRef) (resolve.Object, error) {
	if boardRef.id != "" || boardRef.name != "" {
		return a.board(ctx, boardRef)
	}
	for _, ref := range refs {
		if ref.needsLookup() {
			return a.board(ctx, boardRef)
		}
	}
	return resolve.Object{}, nil
}

// resolver builds the name resolver for this invocation.
func (a *App) resolver(ctx context.Context) (*resolve.Resolver, error) {
	if a.res != nil {
		return a.res, nil
	}
	client, err := a.trelloClient(ctx)
	if err != nil {
		return nil, err
	}
	var cache *resolve.Cache
	if !a.noCache {
		// Keyed by the token so a personal credential never reads a work
		// workspace's index, which would resolve a name to an id on a board
		// the caller cannot even see.
		cache = resolve.NewCache(a.token, 0)
	}
	// Strictness comes from the command's own annotation rather than from the
	// caller remembering to ask for it. It was opt-in, and `labels delete`
	// shipped without it — a destructive command that happily acted on a
	// prefix. Anything a tree walk can decide should not be a thing to
	// remember.
	a.res = &resolve.Resolver{Fetcher: client, Cache: cache, Fuzzy: a.fuzzy, Strict: a.strictResolution}
	return a.res, nil
}

// board resolves the board a command should act on.
//
// Precedence is --board-id, then --board, then TRELLO_CLI_BOARD. A default
// exists because most callers work in one board and repeating it on every
// invocation is pure overhead.
func (a *App) board(ctx context.Context, ref objectRef) (resolve.Object, error) {
	if ref.id != "" {
		return resolve.Object{ID: ref.id}, nil
	}
	query := ref.name
	if query == "" {
		query = a.cfg.DefaultBoard
	}
	if query == "" {
		return resolve.Object{}, errx.Usage("a board is required: pass --board, --board-id, or set TRELLO_CLI_BOARD")
	}
	r, err := a.resolver(ctx)
	if err != nil {
		return resolve.Object{}, err
	}
	return r.Board(ctx, query)
}

// list resolves a list within a board.
func (a *App) list(ctx context.Context, boardID string, ref objectRef) (resolve.Object, error) {
	if ref.id != "" {
		return resolve.Object{ID: ref.id}, nil
	}
	query := ref.name
	if query == "" {
		query = a.cfg.DefaultList
	}
	if query == "" {
		return resolve.Object{}, errx.Usage("a list is required: pass --list, --list-id, or set TRELLO_CLI_LIST")
	}
	r, err := a.resolver(ctx)
	if err != nil {
		return resolve.Object{}, err
	}
	return r.List(ctx, boardID, query)
}

// card resolves a card within a board.
func (a *App) card(ctx context.Context, boardID string, ref objectRef) (resolve.Object, error) {
	if ref.id != "" {
		return resolve.Object{ID: ref.id}, nil
	}
	if ref.name == "" {
		return resolve.Object{}, errx.Usage("a card is required: pass --card or --card-id")
	}
	r, err := a.resolver(ctx)
	if err != nil {
		return resolve.Object{}, err
	}
	return r.Card(ctx, boardID, ref.name)
}

// verify runs use against a resolved object, refreshing the index once if the
// id turns out to be gone.
//
// A cache hit must never be authoritative: an id read from a stale index can
// name an object someone has since deleted, and without this the caller gets a
// not-found for a name that does resolve live. It lives here rather than in
// each command body because an opt-in safety rail is the same defect class as
// a hand-built command that skips the confirmation gate — it works until the
// one place someone forgets.
// invalidate drops every index the resolution actually read, and only those.
func (a *App) verify(
	ctx context.Context,
	invalidate func(),
	resolveFn func(context.Context) (resolve.Object, error),
	use func(context.Context, resolve.Object) error,
) error {
	// A resolution can fail because an id it was handed was rejected, which is
	// the stale-index case and worth another go, or because a name matched
	// nothing, which is not.
	obj, err := resolveFn(ctx)
	if err != nil && !staleID(err) {
		return err
	}
	if err == nil {
		useErr := use(ctx, obj)
		if useErr == nil || !staleID(useErr) {
			return useErr
		}
	}
	invalidate()
	if obj, err = resolveFn(ctx); err != nil {
		return err
	}
	return use(ctx, obj)
}

// staleID reports whether err is an id being rejected rather than a name
// failing to match.
//
// Both are exit 3 and they mean opposite things. The client raises NOT_FOUND
// when Trello rejects an id it was handed, which is exactly what a stale index
// looks like. The resolver raises NOT_FOUND_<KIND> only after listing the live
// objects and finding none that match, so the id it looked within was good and
// the name simply is not there. Retrying that second case refreshes an index
// that was already correct, costs a round trip, and throws away a good answer
// to ask the same question twice.
func staleID(err error) bool {
	var typed *errx.Error
	return errors.As(err, &typed) && typed.Reason == "NOT_FOUND"
}
