package cli

import (
	"context"

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

// bind registers --<kind> and --<kind>-id on cmd.
func (r *objectRef) bind(cmd *cobra.Command, kind, help string) {
	cmd.Flags().StringVar(&r.name, kind, "", help)
	cmd.Flags().StringVar(&r.id, kind+"-id", "", "exact "+kind+" id, skipping name resolution")
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
	a.res = &resolve.Resolver{Fetcher: client, Cache: cache, Fuzzy: a.fuzzy}
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

// withFreshIndex resolves an object and uses it, refreshing the index once if
// the id turns out to be gone.
//
// This is what keeps a cache hit from ever being authoritative. An id read from
// a stale index can name an object someone has since deleted; without the
// retry the caller would get a not-found for a name that does exist.
func withFreshIndex(
	ctx context.Context,
	r *resolve.Resolver,
	scope string,
	resolveFn func(context.Context) (resolve.Object, error),
	use func(context.Context, resolve.Object) error,
) error {
	obj, err := resolveFn(ctx)
	if err != nil {
		return err
	}
	err = use(ctx, obj)
	if err == nil || errx.ExitCode(err) != errx.CodeNotFound {
		return err
	}
	r.Invalidate(scope)
	if obj, err = resolveFn(ctx); err != nil {
		return err
	}
	return use(ctx, obj)
}
