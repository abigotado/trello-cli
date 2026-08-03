package cli

import (
	"context"

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
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
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
	getCmd := a.newCommand(get, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		r, err := a.resolver(ctx)
		if err != nil {
			return err
		}
		var board trello.Board
		err = withFreshIndex(ctx, r, resolve.BoardScope(),
			func(ctx context.Context) (resolve.Object, error) { return a.board(ctx, ref) },
			func(ctx context.Context, obj resolve.Object) error {
				board, err = client.Board(ctx, obj.ID)
				return err
			})
		if err != nil {
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

	sub := a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		r, err := a.resolver(ctx)
		if err != nil {
			return err
		}
		var lists []trello.List
		err = withFreshIndex(ctx, r, resolve.BoardScope(),
			func(ctx context.Context) (resolve.Object, error) { return a.board(ctx, boardRef) },
			func(ctx context.Context, obj resolve.Object) error {
				lists, err = client.Lists(ctx, obj.ID, includeClosed)
				return err
			})
		if err != nil {
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
	var boardRef, listRef, cardRef objectRef
	var includeClosed bool

	list := &cobra.Command{
		Use:   "list",
		Short: "List cards on a board, or in one list",
		Args:  cobra.NoArgs,
	}
	boardRef.bind(list, "board", "board name, id, or shortLink")
	listRef.bind(list, "list", "restrict to one list, by name or id")
	list.Flags().BoolVar(&includeClosed, "all", false, "include archived cards")

	listCmd := a.newCommand(list, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		board, err := a.board(ctx, boardRef)
		if err != nil {
			return err
		}
		// Fetched once and used to label every card, so a listing does not
		// force the caller into a second lookup just to read it.
		names, err := a.listNames(ctx, board.ID)
		if err != nil {
			return err
		}

		var cards []trello.Card
		if listRef.empty() {
			cards, err = client.CardsOnBoard(ctx, board.ID, includeClosed)
		} else {
			var target resolve.Object
			if target, err = a.list(ctx, board.ID, listRef); err == nil {
				cards, err = client.CardsInList(ctx, target.ID, includeClosed)
			}
		}
		if err != nil {
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
	boardRef.bind(get, "board", "board to resolve the card name within")
	cardRef.bind(get, "card", "card name, id, or shortLink")

	getCmd := a.newCommand(get, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		obj, err := a.cardAnywhere(ctx, boardRef, cardRef)
		if err != nil {
			return err
		}
		card, err := client.Card(ctx, obj.ID)
		if err != nil {
			return err
		}
		view := cardView{Card: card}
		// Only worth another request when the caller gave a board to resolve
		// within; a bare card id should stay a single round trip.
		if !boardRef.empty() || a.cfg.DefaultBoard != "" {
			if board, err := a.board(ctx, boardRef); err == nil {
				if names, err := a.listNames(ctx, board.ID); err == nil {
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

	sub := a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		board, err := a.board(ctx, boardRef)
		if err != nil {
			return err
		}
		labels, err := client.Labels(ctx, board.ID)
		if err != nil {
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

	sub := a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		board, err := a.board(ctx, boardRef)
		if err != nil {
			return err
		}
		members, err := client.BoardMembers(ctx, board.ID)
		if err != nil {
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
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum comments to return (server default when unset)")
	cmd.Flags().StringVar(&before, "before", "", "return only comments older than this comment id or ISO date")

	sub := a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		obj, err := a.cardAnywhere(ctx, boardRef, cardRef)
		if err != nil {
			return err
		}
		comments, err := client.Comments(ctx, obj.ID, trello.Page{Limit: limit, Before: before})
		if err != nil {
			return err
		}
		views := make([]commentView, 0, len(comments))
		for _, c := range comments {
			views = append(views, commentView{c})
		}
		return a.out.Success(views)
	})
	return group("comments", "Work with card comments", append([]*cobra.Command{sub}, a.commentsWriteCommands()...)...)
}

func (a *App) newChecklistsCommand() *cobra.Command {
	var boardRef, cardRef objectRef
	cmd := &cobra.Command{Use: "list", Short: "List the checklists on a card", Args: cobra.NoArgs}
	boardRef.bind(cmd, "board", "board to resolve the card name within")
	cardRef.bind(cmd, "card", "card name, id, or shortLink")

	sub := a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		obj, err := a.cardAnywhere(ctx, boardRef, cardRef)
		if err != nil {
			return err
		}
		lists, err := client.Checklists(ctx, obj.ID)
		if err != nil {
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

	sub := a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		obj, err := a.cardAnywhere(ctx, boardRef, cardRef)
		if err != nil {
			return err
		}
		items, err := client.Attachments(ctx, obj.ID)
		if err != nil {
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
		return a.out.Success(views)
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
			// Deliberately does not require credentials: clearing a cache must
			// work even when the credential that created it is gone.
			cache := resolve.NewCache(a.token, 0)
			if a.dryRun {
				return a.out.Success(cacheStatusView{Path: cache.Path(), Cleared: false})
			}
			if err := cache.Clear(); err != nil {
				return errx.Internal("clear cache: %v", err)
			}
			return a.out.Success(cacheStatusView{Path: cache.Path(), Cleared: true})
		},
	)
	return group("cache", "Manage the local name-resolution index", clear)
}

type cacheStatusView struct {
	Path    string `json:"path"`
	Cleared bool   `json:"cleared"`
}

func (c cacheStatusView) Fields() []output.Field {
	state := "unchanged"
	if c.Cleared {
		state = "cleared"
	}
	return []output.Field{
		{Name: "cleared", Value: state, Raw: c.Cleared},
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
