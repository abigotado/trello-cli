package cli

import (
	"context"
	"strconv"

	"github.com/abigotado/trello-cli/internal/errx"
	"github.com/abigotado/trello-cli/internal/output"
	"github.com/abigotado/trello-cli/internal/resolve"
	"github.com/abigotado/trello-cli/internal/trello"
	"github.com/spf13/cobra"
)

// readCommands returns the top-level command groups. Each group holds its
// read subcommands and, from write.go, its mutating ones.
func (a *App) readCommands() []*cobra.Command {
	return []*cobra.Command{
		a.newBoardsCommand(),
		a.newListsCommand(),
		a.newCardsCommand(),
		a.newLabelsCommand(),
		a.newMembersCommand(),
		a.newCommentsCommand(),
		a.newChecklistsCommand(),
		a.newAttachmentsCommand(),
		a.newActivityCommand(),
		a.newSearchCommand(),
		a.newCacheCommand(),
	}
}

// group builds a parent command whose only job is to hold subcommands.
func group(use, short string, subs ...*cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  usageArgs(cobra.NoArgs),
		// A group is not runnable. Printing help and exiting 0 would tell a
		// machine caller the command succeeded and hand it usage prose to
		// parse; see the root command for the same reasoning.
		RunE: func(c *cobra.Command, _ []string) error {
			return errx.Usage("%s needs a subcommand", c.CommandPath())
		},
	}
	cmd.AddCommand(subs...)
	return cmd
}

func (a *App) newBoardsCommand() *cobra.Command {
	var includeClosed bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List the boards you can see",
		Args:  cobra.NoArgs,
	}
	list.Flags().BoolVar(&includeClosed, "all", false, "include closed boards")
	listCmd := a.newCommand(list, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		boards, err := client.Boards(ctx, includeClosed)
		if err != nil {
			return err
		}
		views := make([]boardView, 0, len(boards))
		for _, b := range boards {
			views = append(views, boardView{b})
		}
		return a.out.Success(views)
	})

	var ref objectRef
	get := &cobra.Command{
		Use:   "get",
		Short: "Show one board",
		Args:  cobra.NoArgs,
	}
	ref.bind(get, "board", "board name, id, or shortLink")
	getCmd := a.newCommand(requires(get, "board"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		var board trello.Board
		if err := a.onBoard(ctx, ref, func(ctx context.Context, obj resolve.Object) error {
			var getErr error
			board, getErr = client.Board(ctx, obj.ID)
			return getErr
		}); err != nil {
			return err
		}
		return a.out.Success(boardView{board})
	})

	return group("boards", "Work with boards", listCmd, getCmd)
}

func (a *App) newListsCommand() *cobra.Command {
	var boardRef objectRef
	var includeClosed bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the lists on a board",
		Args:  cobra.NoArgs,
	}
	boardRef.bind(cmd, "board", "board name, id, or shortLink")
	cmd.Flags().BoolVar(&includeClosed, "all", false, "include archived lists")

	sub := a.newCommand(requires(cmd, "board"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		var lists []trello.List
		if err := a.onBoard(ctx, boardRef, func(ctx context.Context, obj resolve.Object) error {
			var listErr error
			lists, listErr = client.Lists(ctx, obj.ID, includeClosed)
			return listErr
		}); err != nil {
			return err
		}
		views := make([]listView, 0, len(lists))
		for _, l := range lists {
			views = append(views, listView{l})
		}
		return a.out.Success(views)
	})
	return group("lists", "Work with lists", append([]*cobra.Command{sub}, a.listsWriteCommands()...)...)
}

func (a *App) newCardsCommand() *cobra.Command {
	// One objectRef per subcommand, never shared. cobra writes a bound flag
	// straight into the variable, so binding one ref to two commands makes
	// them alias each other's parsed value the moment more than one command
	// runs in a process.
	var listBoardRef, listRef objectRef
	var getBoardRef, cardRef objectRef
	var includeClosed bool

	list := &cobra.Command{
		Use:   "list",
		Short: "List cards on a board, or in one list",
		Args:  cobra.NoArgs,
	}
	listBoardRef.bind(list, "board", "board name, id, or shortLink")
	listRef.bind(list, "list", "restrict to one list, by name or id")
	list.Flags().BoolVar(&includeClosed, "all", false, "include archived cards")

	listCmd := a.newCommand(requires(list, "board"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		var cards []trello.Card
		var names map[string]string
		if err := a.onBoard(ctx, listBoardRef, func(ctx context.Context, board resolve.Object) error {
			// Fetched once and used to label every card, so a listing does not
			// force the caller into a second lookup just to read it.
			var err error
			if names, err = a.listNames(ctx, board.ID); err != nil {
				return err
			}
			// a.list falls back to TRELLO_CLI_LIST, so checking empty() directly
			// would give one env var two meanings: honoured by cards create and
			// silently ignored here.
			if listRef.empty() && a.cfg.DefaultList == "" {
				cards, err = client.CardsOnBoard(ctx, board.ID, includeClosed)
				return err
			}
			// The list index is its own scope, and a stale id in it fails the
			// same way a stale board id does.
			return a.verify(ctx, resolve.ListScope(board.ID),
				func(ctx context.Context) (resolve.Object, error) { return a.list(ctx, board.ID, listRef) },
				func(ctx context.Context, target resolve.Object) error {
					cards, err = client.CardsInList(ctx, target.ID, includeClosed)
					return err
				})
		}); err != nil {
			return err
		}
		views := make([]cardView, 0, len(cards))
		for _, c := range cards {
			views = append(views, cardView{Card: c, listName: names[c.IDList]})
		}
		return a.out.Success(views)
	})

	get := &cobra.Command{
		Use:   "get",
		Short: "Show one card",
		Args:  cobra.NoArgs,
	}
	getBoardRef.bind(get, "board", "board to resolve the card name within")
	cardRef.bind(get, "card", "card name, id, or shortLink")

	getCmd := a.newCommand(requires(get, "card"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		var card trello.Card
		if err := a.onCard(ctx, getBoardRef, cardRef, func(ctx context.Context, obj resolve.Object) error {
			var getErr error
			card, getErr = client.Card(ctx, obj.ID)
			return getErr
		}); err != nil {
			return err
		}
		view := cardView{Card: card}
		// Only when the caller named a board on this invocation. Including the
		// configured default here cost two extra requests on every
		// `cards get --card-id X`, to label the card with a list from a board
		// it may not even be on — the opposite of the comment that used to sit
		// here. Errors are ignored deliberately: the label is a convenience,
		// and failing the whole read because it could not be fetched would be
		// worse than omitting it.
		if !getBoardRef.empty() {
			if board, boardErr := a.board(ctx, getBoardRef); boardErr == nil {
				if names, nameErr := a.listNames(ctx, board.ID); nameErr == nil {
					view.listName = names[card.IDList]
				}
			}
		}
		return a.out.Success(view)
	})

	return group("cards", "Work with cards", append([]*cobra.Command{listCmd, getCmd}, a.cardsWriteCommands()...)...)
}

func (a *App) newLabelsCommand() *cobra.Command {
	var boardRef objectRef
	cmd := &cobra.Command{Use: "list", Short: "List the labels defined on a board", Args: cobra.NoArgs}
	boardRef.bind(cmd, "board", "board name, id, or shortLink")

	sub := a.newCommand(requires(cmd, "board"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		var labels []trello.Label
		if err := a.onBoard(ctx, boardRef, func(ctx context.Context, obj resolve.Object) error {
			var labelErr error
			labels, labelErr = client.Labels(ctx, obj.ID)
			return labelErr
		}); err != nil {
			return err
		}
		views := make([]labelView, 0, len(labels))
		for _, l := range labels {
			views = append(views, labelView{l})
		}
		return a.out.Success(views)
	})
	return group("labels", "Work with labels", append([]*cobra.Command{sub}, a.labelsWriteCommands()...)...)
}

func (a *App) newMembersCommand() *cobra.Command {
	var boardRef objectRef
	cmd := &cobra.Command{Use: "list", Short: "List the members of a board", Args: cobra.NoArgs}
	boardRef.bind(cmd, "board", "board name, id, or shortLink")

	sub := a.newCommand(requires(cmd, "board"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		var members []trello.Member
		if err := a.onBoard(ctx, boardRef, func(ctx context.Context, obj resolve.Object) error {
			var memberErr error
			members, memberErr = client.BoardMembers(ctx, obj.ID)
			return memberErr
		}); err != nil {
			return err
		}
		views := make([]memberListView, 0, len(members))
		for _, m := range members {
			views = append(views, memberListView{m})
		}
		return a.out.Success(views)
	})
	return group("members", "Work with board members", append([]*cobra.Command{sub}, a.membersWriteCommands()...)...)
}

func (a *App) newCommentsCommand() *cobra.Command {
	var boardRef, cardRef objectRef
	var limit int
	var before string

	cmd := &cobra.Command{Use: "list", Short: "List the comments on a card", Args: cobra.NoArgs}
	boardRef.bind(cmd, "board", "board to resolve the card name within")
	cardRef.bind(cmd, "card", "card name, id, or shortLink")
	// Trello caps actions at 1000 per call, so a card with a long history needs
	// both a bound and a cursor.
	cmd.Flags().IntVar(&limit, "limit", trello.DefaultActionsPageSize, "maximum comments to return (1-1000)")
	cmd.Flags().StringVar(&before, "before", "", "return only comments older than this comment id or ISO date")

	sub := a.newCommand(requires(cmd, "card"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		if err := checkActionLimit(limit); err != nil {
			return err
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		var comments []trello.Comment
		if err := a.onCard(ctx, boardRef, cardRef, func(ctx context.Context, obj resolve.Object) error {
			var commentErr error
			comments, commentErr = client.Comments(ctx, obj.ID, trello.Page{Limit: limit, Before: before})
			return commentErr
		}); err != nil {
			return err
		}
		views := make([]commentView, 0, len(comments))
		for _, c := range comments {
			views = append(views, commentView{c})
		}
		// A page that came back full is the only signal Trello gives that more
		// may exist. Reporting truncated:false there would tell the caller it
		// had everything. See activity list for why this is not an equality.
		return a.out.SuccessPage(views, len(comments) >= limit)
	})
	return group("comments", "Work with card comments", append([]*cobra.Command{sub}, a.commentsWriteCommands()...)...)
}

func (a *App) newActivityCommand() *cobra.Command {
	var boardRef objectRef
	var limit int
	var since, before, filter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List board activity: card creations and list moves",
		Long: "List a board's activity, newest first: card creations and\n" +
			"list-to-list moves.\n\n" +
			"Trello's activity feed also carries comments, label and due-date edits,\n" +
			"member changes, and more. Those are noise for the report this command\n" +
			"exists to build, so the default --type is 'createCard,updateCard:idList'.\n" +
			"Pass --type with Trello's action-type names to widen or replace it. On a\n" +
			"widened feed the 'changed' field names which card fields an update\n" +
			"touched, since Trello types a move, a rename, and an archive alike.\n\n" +
			"--since and --before each take an action id or an ISO 8601 date. The\n" +
			"feed is newest-first and --limit cuts from the old end, so --since sets\n" +
			"a floor rather than a window: page backwards with --before, carrying the\n" +
			"oldest id you got, until a page comes back with truncated false.",
		Args: cobra.NoArgs,
	}
	boardRef.bind(cmd, "board", "board name, id, or shortLink")
	cmd.Flags().StringVar(&since, "since", "", "return only activity newer than this action id or ISO date")
	cmd.Flags().StringVar(&before, "before", "", "return only activity older than this action id or ISO date")
	cmd.Flags().StringVar(&filter, "type", "", "comma-separated Trello action types (default: createCard,updateCard:idList)")
	cmd.Flags().IntVar(&limit, "limit", trello.DefaultActionsPageSize, "maximum items to return (1-1000)")

	sub := a.newCommand(requires(cmd, "board"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		if err := checkActionLimit(limit); err != nil {
			return err
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		var items []trello.Activity
		if err := a.onBoard(ctx, boardRef, func(ctx context.Context, obj resolve.Object) error {
			var actErr error
			items, actErr = client.Activity(ctx, obj.ID, filter, trello.Page{Limit: limit, Since: since, Before: before})
			return actErr
		}); err != nil {
			return err
		}
		views := make([]activityView, 0, len(items))
		for _, it := range items {
			views = append(views, activityView{it})
		}
		// Same signal as comments list: a page that came back full is the only
		// hint Trello gives that more may exist. Greater-than rather than
		// equal because a proxy or a replay mock on TRELLO_CLI_BASE_URL can
		// over-deliver, and reading that as an unfilled page would report the
		// history complete.
		return a.out.SuccessPage(views, len(items) >= limit)
	})
	return group("activity", "Work with board activity", sub)
}

// checkActionLimit rejects a --limit an actions feed cannot honor.
//
// Both bounds matter for the same reason. A non-positive limit used to be
// dropped from the request, leaving Trello to apply its own page size while the
// envelope still said truncated:false — the caller was handed the newest page
// and told it was the whole history. A limit above the cap comes back short of
// what was asked for, which reads as an unfilled page and reports the same lie.
func checkActionLimit(limit int) error {
	if limit < 1 || limit > trello.MaxActionsPerPage {
		return errx.Usage("--limit must be between 1 and %d", trello.MaxActionsPerPage)
	}
	return nil
}

func (a *App) newChecklistsCommand() *cobra.Command {
	var boardRef, cardRef objectRef
	cmd := &cobra.Command{Use: "list", Short: "List the checklists on a card", Args: cobra.NoArgs}
	boardRef.bind(cmd, "board", "board to resolve the card name within")
	cardRef.bind(cmd, "card", "card name, id, or shortLink")

	sub := a.newCommand(requires(cmd, "card"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		var lists []trello.Checklist
		if err := a.onCard(ctx, boardRef, cardRef, func(ctx context.Context, obj resolve.Object) error {
			var listErr error
			lists, listErr = client.Checklists(ctx, obj.ID)
			return listErr
		}); err != nil {
			return err
		}
		views := make([]checklistView, 0, len(lists))
		for _, c := range lists {
			views = append(views, checklistView{c})
		}
		return a.out.Success(views)
	})
	return group("checklists", "Work with card checklists", append([]*cobra.Command{sub}, a.checklistsWriteCommands()...)...)
}

func (a *App) newAttachmentsCommand() *cobra.Command {
	var boardRef, cardRef objectRef
	cmd := &cobra.Command{Use: "list", Short: "List the attachments on a card", Args: cobra.NoArgs}
	boardRef.bind(cmd, "board", "board to resolve the card name within")
	cardRef.bind(cmd, "card", "card name, id, or shortLink")

	sub := a.newCommand(requires(cmd, "card"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		var items []trello.Attachment
		if err := a.onCard(ctx, boardRef, cardRef, func(ctx context.Context, obj resolve.Object) error {
			var attErr error
			items, attErr = client.Attachments(ctx, obj.ID)
			return attErr
		}); err != nil {
			return err
		}
		views := make([]attachmentView, 0, len(items))
		for _, at := range items {
			views = append(views, attachmentView{at})
		}
		return a.out.Success(views)
	})
	return group("attachments", "Work with card attachments", append([]*cobra.Command{sub}, a.attachmentsWriteCommands()...)...)
}

func (a *App) newSearchCommand() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search your boards and cards",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Flags().IntVar(&limit, "limit", 10, "maximum results of each kind")

	return a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, args []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		results, err := client.Search(ctx, args[0], limit)
		if err != nil {
			return err
		}
		views := make([]searchHitView, 0, len(results.Boards)+len(results.Cards))
		for _, b := range results.Boards {
			views = append(views, searchHitView{Kind: "board", ID: b.ID, ShortLink: b.ShortLink, Name: b.Name, URL: b.URL})
		}
		for _, c := range results.Cards {
			views = append(views, searchHitView{Kind: "card", ID: c.ID, ShortLink: c.ShortLink, Name: c.Name, URL: c.URL})
		}
		truncated := limit > 0 && (len(results.Boards) == limit || len(results.Cards) == limit)
		return a.out.SuccessPage(views, truncated)
	})
}

func (a *App) newCacheCommand() *cobra.Command {
	clear := a.newCommand(
		&cobra.Command{
			Use:   "clear",
			Short: "Delete the local name-resolution index",
			Args:  cobra.NoArgs,
		},
		func(ctx context.Context, _ *cobra.Command, _ []string) error {
			// Works from the cache directory, not from a Cache value. This
			// command deliberately does not load credentials, and an index
			// filename is keyed by a hash of the token: a Cache built without
			// one addresses a file that never existed, so it reported success
			// while the real index survived. Clearing has to work when the
			// credential that wrote the index is gone.
			if a.dryRun {
				dir, pending, err := resolve.Pending()
				if err != nil {
					return errx.Internal("inspect cache: %v", err)
				}
				return a.out.Success(cacheStatusView{Path: dir, Removed: pending, Cleared: false})
			}
			dir, removed, err := resolve.ClearAll()
			if err != nil {
				return errx.Internal("clear cache: %v", err)
			}
			return a.out.Success(cacheStatusView{Path: dir, Removed: removed, Cleared: removed > 0})
		},
	)
	return group("cache", "Manage the local name-resolution index", clear)
}

// cacheStatusView reports what a clear did.
//
// removed is the useful number: cleared was previously true on every run,
// including the runs that removed nothing, which is what let a broken clear
// look like a working one. cleared now means "something went away", and
// removed says how much. path is the directory, since one index exists per
// stored account.
type cacheStatusView struct {
	Path    string `json:"path"`
	Removed int    `json:"removed"`
	Cleared bool   `json:"cleared"`
}

func (c cacheStatusView) Fields() []output.Field {
	state := "unchanged"
	if c.Cleared {
		state = "cleared"
	}
	return []output.Field{
		{Name: "cleared", Value: state, Raw: c.Cleared},
		{Name: "removed", Value: strconv.Itoa(c.Removed), Raw: c.Removed},
		{Name: "path", Value: c.Path, Raw: c.Path},
	}
}

// cardAnywhere resolves a card, using a board only when one is needed.
//
// A card given by id or shortLink is addressable directly, so requiring a
// board there would cost a round trip and force the caller to know something
// it does not need to.
func (a *App) cardAnywhere(ctx context.Context, boardRef, cardRef objectRef) (resolve.Object, error) {
	if cardRef.id != "" {
		return resolve.Object{ID: cardRef.id}, nil
	}
	if cardRef.name == "" {
		return resolve.Object{}, errx.Usage("a card is required: pass --card or --card-id")
	}
	// Only a 24-hex id is unambiguous enough to skip board resolution. A
	// shortLink-shaped value could equally be an eight-character card name.
	if resolve.LooksLikeID(cardRef.name) {
		return resolve.Object{ID: cardRef.name}, nil
	}
	board, err := a.board(ctx, boardRef)
	if err != nil {
		return resolve.Object{}, err
	}
	return a.card(ctx, board.ID, cardRef)
}

// onBoard runs use against the board a command names, refreshing the board
// index once and retrying if the id it resolved turns out to be gone.
//
// Every read that resolves a board goes through this rather than calling
// a.board directly. verify's own comment says why: a rail each command body
// has to remember to opt into works until the one place someone forgets, and
// the failure there is a not-found for a name that resolves perfectly well.
func (a *App) onBoard(ctx context.Context, ref objectRef, use func(context.Context, resolve.Object) error) error {
	return a.verify(ctx, resolve.BoardScope(),
		func(ctx context.Context) (resolve.Object, error) { return a.board(ctx, ref) },
		use)
}

// onCard is the same rail for a card.
//
// Two indexes sit on the path to a card named by name: the board it is looked
// up within, and that board's card index. Either can hold a dead id, so the
// rails nest and whichever one actually went bad is the one refreshed. Wrapping
// only the card index leaves a stale board id surfacing as a not-found for a
// card that resolves fine.
func (a *App) onCard(ctx context.Context, boardRef, cardRef objectRef, use func(context.Context, resolve.Object) error) error {
	// A card named by id or shortLink came out of no index at all, so there is
	// nothing that can be stale and use runs once.
	if cardRef.id != "" {
		return use(ctx, resolve.Object{ID: cardRef.id})
	}
	if cardRef.name == "" {
		return errx.Usage("a card is required: pass --card or --card-id")
	}
	if resolve.LooksLikeID(cardRef.name) {
		return use(ctx, resolve.Object{ID: cardRef.name})
	}
	return a.onBoard(ctx, boardRef, func(ctx context.Context, board resolve.Object) error {
		return a.verify(ctx, resolve.CardScope(board.ID),
			func(ctx context.Context) (resolve.Object, error) { return a.card(ctx, board.ID, cardRef) },
			use)
	})
}

// listNames maps list ids to names for labelling a card listing.
func (a *App) listNames(ctx context.Context, boardID string) (map[string]string, error) {
	client, err := a.trelloClient(ctx)
	if err != nil {
		return nil, err
	}
	lists, err := client.Lists(ctx, boardID, true)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(lists))
	for _, l := range lists {
		names[l.ID] = l.Name
	}
	return names, nil
}
