package cli

import (
	"context"

	"github.com/abigotado/trello-cli/internal/errx"
	"github.com/abigotado/trello-cli/internal/output"
	"github.com/abigotado/trello-cli/internal/resolve"
	"github.com/abigotado/trello-cli/internal/trello"
	"github.com/spf13/cobra"
)

// mutating marks a command as changing remote state, which gates it behind
// TRELLO_CLI_READONLY.
func mutating(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationMutates] = "true"
	return cmd
}

// destructive marks a command whose effect this tool cannot undo, which
// additionally requires --yes.
func destructive(cmd *cobra.Command) *cobra.Command {
	cmd = mutating(cmd)
	cmd.Annotations[annotationDestructive] = "true"
	return cmd
}

// planView is what --dry-run prints.
//
// It reports the ids every name resolved to, which is the whole point: a dry
// run that only echoed the caller's own input back would validate nothing.
type planView struct {
	Action string            `json:"action"`
	Target map[string]string `json:"target"`
	// No omitempty: -o raw marshals this struct directly while -o json goes
	// through Fields(), and omitempty would make raw drop a key json keeps.
	Changes map[string]string `json:"changes"`
	DryRun  bool              `json:"dryRun"`
}

func (p planView) Fields() []output.Field {
	return []output.Field{
		{Name: "dryRun", Value: "dry-run", Raw: true},
		{Name: "action", Value: p.Action, Raw: p.Action},
		{Name: "target", Value: kvText(p.Target), Raw: p.Target},
		{Name: "changes", Value: kvText(p.Changes), Raw: p.Changes},
	}
}

func kvText(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	out := ""
	for _, k := range sortedKeys(m) {
		if out != "" {
			out += " "
		}
		out += k + "=" + m[k]
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// plan renders the dry-run result.
func (a *App) plan(action string, target, changes map[string]string) error {
	return a.out.Success(planView{Action: action, Target: target, Changes: changes, DryRun: true})
}

// strictResolver returns a resolver that refuses to match a prefix.
func (a *App) strictResolver(ctx context.Context) (*resolve.Resolver, error) {
	r, err := a.resolver(ctx)
	if err != nil {
		return nil, err
	}
	strict := *r
	strict.Strict = true
	return &strict, nil
}

// ---- lists ----

func (a *App) listsWriteCommands() []*cobra.Command {
	var boardRef objectRef
	var name, pos string
	create := &cobra.Command{Use: "create", Short: "Create a list on a board", Args: cobra.NoArgs}
	boardRef.bind(create, "board", "board name, id, or shortLink")
	create.Flags().StringVar(&name, "name", "", "list name")
	create.Flags().StringVar(&pos, "pos", "", "position: top, bottom, or a number")

	createCmd := a.newCommand(requires(mutating(create), "board", "name"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		if name == "" {
			return errx.Usage("--name is required")
		}
		board, err := a.board(ctx, boardRef)
		if err != nil {
			return err
		}
		if a.dryRun {
			return a.plan("lists create",
				map[string]string{"board": board.ID},
				map[string]string{"name": name})
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		created, err := client.CreateList(ctx, board.ID, name, pos)
		if err != nil {
			return err
		}
		// The new list changes the index this board's names resolve against.
		a.invalidate(resolve.ListScope(board.ID))
		return a.out.Success(listView{created})
	})

	var archiveBoardRef, archiveListRef objectRef
	var restore bool
	archive := &cobra.Command{
		Use:   "archive",
		Short: "Archive a list, or restore it with --restore",
		Args:  cobra.NoArgs,
	}
	archiveBoardRef.bind(archive, "board", "board name, id, or shortLink")
	archiveListRef.bind(archive, "list", "list name or id")
	archive.Flags().BoolVar(&restore, "restore", false, "restore an archived list instead")

	// Archiving is reversible and Trello offers no list delete, so this is
	// mutating but not destructive: requiring --yes here would be noise.
	archiveCmd := a.newCommand(requires(mutating(archive), "board", "list"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		board, err := a.board(ctx, archiveBoardRef)
		if err != nil {
			return err
		}
		target, err := a.list(ctx, board.ID, archiveListRef)
		if err != nil {
			return err
		}
		action := "lists archive"
		if restore {
			action = "lists restore"
		}
		if a.dryRun {
			return a.plan(action, map[string]string{"board": board.ID, "list": target.ID}, nil)
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		updated, err := client.SetListClosed(ctx, target.ID, !restore)
		if err != nil {
			return err
		}
		a.invalidate(resolve.ListScope(board.ID))
		return a.out.Success(listView{updated})
	})

	return []*cobra.Command{createCmd, archiveCmd}
}

// ---- cards ----

func (a *App) cardsWriteCommands() []*cobra.Command {
	return []*cobra.Command{
		a.cardCreateCommand(),
		a.cardUpdateCommand(),
		a.cardMoveCommand(),
		a.cardArchiveCommand(),
		a.cardDeleteCommand(),
	}
}

func (a *App) cardCreateCommand() *cobra.Command {
	var boardRef, listRef objectRef
	var name, desc, due, pos string
	cmd := &cobra.Command{Use: "create", Short: "Create a card in a list", Args: cobra.NoArgs}
	boardRef.bind(cmd, "board", "board name, id, or shortLink")
	listRef.bind(cmd, "list", "list name or id")
	cmd.Flags().StringVar(&name, "name", "", "card name")
	cmd.Flags().StringVar(&desc, "desc", "", "card description")
	cmd.Flags().StringVar(&due, "due", "", "due date, ISO 8601")
	cmd.Flags().StringVar(&pos, "pos", "", "position: top, bottom, or a number")

	return a.newCommand(requires(mutating(cmd), "board", "list", "name"), func(ctx context.Context, c *cobra.Command, _ []string) error {
		if name == "" {
			return errx.Usage("--name is required")
		}
		board, err := a.board(ctx, boardRef)
		if err != nil {
			return err
		}
		target, err := a.list(ctx, board.ID, listRef)
		if err != nil {
			return err
		}
		in := trello.CardInput{Name: &name}
		changes := map[string]string{"name": name}
		if c.Flags().Changed("desc") {
			in.Desc = &desc
			changes["desc"] = desc
		}
		if c.Flags().Changed("due") {
			in.Due = &due
			changes["due"] = due
		}
		if c.Flags().Changed("pos") {
			in.Pos = &pos
		}
		if a.dryRun {
			return a.plan("cards create",
				map[string]string{"board": board.ID, "list": target.ID}, changes)
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		created, err := client.CreateCard(ctx, target.ID, in)
		if err != nil {
			return err
		}
		a.invalidate(resolve.CardScope(board.ID))
		// listName from the target this command just resolved, not from a
		// second lookup. Returning it empty made a create report that the card
		// it had placed was in no list at all — the one fact the caller most
		// wants confirmed, and one already in hand.
		return a.out.Success(cardView{Card: created, listName: target.Name})
	})
}

func (a *App) cardUpdateCommand() *cobra.Command {
	var boardRef, cardRef objectRef
	var name, desc, due string
	var dueComplete bool
	var clearDue bool
	cmd := &cobra.Command{Use: "update", Short: "Change a card's fields", Args: cobra.NoArgs}
	boardRef.bind(cmd, "board", "board to resolve the card name within")
	cardRef.bind(cmd, "card", "card name, id, or shortLink")
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&desc, "desc", "", "new description")
	cmd.Flags().StringVar(&due, "due", "", "new due date, ISO 8601")
	cmd.Flags().BoolVar(&dueComplete, "due-complete", false, "mark the due date complete")
	cmd.Flags().BoolVar(&clearDue, "clear-due", false, "remove the due date")

	return a.newCommand(requires(requiresOneOf(mutating(cmd), "name", "desc", "due", "clear-due", "due-complete"), "card"), func(ctx context.Context, c *cobra.Command, _ []string) error {
		if clearDue && c.Flags().Changed("due") {
			return errx.Usage("--due and --clear-due contradict each other")
		}
		obj, err := a.cardAnywhere(ctx, boardRef, cardRef)
		if err != nil {
			return err
		}
		var in trello.CardInput
		changes := map[string]string{}
		if c.Flags().Changed("name") {
			in.Name = &name
			changes["name"] = name
		}
		if c.Flags().Changed("desc") {
			in.Desc = &desc
			changes["desc"] = desc
		}
		if c.Flags().Changed("due") {
			in.Due = &due
			changes["due"] = due
		}
		if clearDue {
			empty := ""
			in.Due = &empty
			changes["due"] = "(cleared)"
		}
		if c.Flags().Changed("due-complete") {
			in.DueComplete = &dueComplete
			changes["due_complete"] = boolWord(dueComplete)
		}
		// An update that changes nothing is a mistake worth reporting rather
		// than a request worth sending.
		if len(changes) == 0 {
			return errx.Usage("nothing to update: pass at least one of --name, --desc, --due, --clear-due, --due-complete")
		}
		if a.dryRun {
			return a.plan("cards update", map[string]string{"card": obj.ID}, changes)
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		updated, err := client.UpdateCard(ctx, obj.ID, in)
		if err != nil {
			return err
		}
		return a.out.Success(cardView{Card: updated})
	})
}

func (a *App) cardMoveCommand() *cobra.Command {
	var boardRef, cardRef, listRef objectRef
	var pos string
	cmd := &cobra.Command{Use: "move", Short: "Move a card to another list", Args: cobra.NoArgs}
	boardRef.bind(cmd, "board", "board name, id, or shortLink")
	cardRef.bind(cmd, "card", "card name, id, or shortLink")
	listRef.bind(cmd, "list", "destination list name or id")
	cmd.Flags().StringVar(&pos, "pos", "", "position in the destination: top, bottom, or a number")

	return a.newCommand(requires(mutating(cmd), "card", "list"), func(ctx context.Context, c *cobra.Command, _ []string) error {
		board, err := a.boardFor(ctx, boardRef, cardRef, listRef)
		if err != nil {
			return err
		}
		card, err := a.card(ctx, board.ID, cardRef)
		if err != nil {
			return err
		}
		target, err := a.list(ctx, board.ID, listRef)
		if err != nil {
			return err
		}
		if a.dryRun {
			return a.plan("cards move",
				map[string]string{"board": board.ID, "card": card.ID},
				map[string]string{"list": target.ID})
		}
		in := trello.CardInput{IDList: &target.ID}
		if c.Flags().Changed("pos") {
			in.Pos = &pos
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		updated, err := client.UpdateCard(ctx, card.ID, in)
		if err != nil {
			return err
		}
		// The destination this command resolved, for the same reason create
		// reports it: "did it land where I asked" is the question a move is
		// asked to answer.
		return a.out.Success(cardView{Card: updated, listName: target.Name})
	})
}

func (a *App) cardArchiveCommand() *cobra.Command {
	var boardRef, cardRef objectRef
	var restore bool
	cmd := &cobra.Command{
		Use:   "archive",
		Short: "Archive a card, or restore it with --restore",
		Args:  cobra.NoArgs,
	}
	boardRef.bind(cmd, "board", "board to resolve the card name within")
	cardRef.bind(cmd, "card", "card name, id, or shortLink")
	cmd.Flags().BoolVar(&restore, "restore", false, "restore an archived card instead")

	// Archiving is reversible from this tool, so it is not destructive.
	return a.newCommand(requires(mutating(cmd), "card"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		obj, err := a.cardAnywhere(ctx, boardRef, cardRef)
		if err != nil {
			return err
		}
		action := "cards archive"
		if restore {
			action = "cards restore"
		}
		if a.dryRun {
			return a.plan(action, map[string]string{"card": obj.ID}, nil)
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		closed := !restore
		updated, err := client.UpdateCard(ctx, obj.ID, trello.CardInput{Closed: &closed})
		if err != nil {
			return err
		}
		return a.out.Success(cardView{Card: updated})
	})
}

func (a *App) cardDeleteCommand() *cobra.Command {
	var boardRef, cardRef objectRef
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Permanently delete a card",
		Long: "Permanently delete a card.\n\n" +
			"Trello has no undo for this, so it requires --yes and refuses to resolve\n" +
			"the target from a name prefix. Use 'cards archive' unless the card really\n" +
			"has to be gone.",
		Args: cobra.NoArgs,
	}
	boardRef.bind(cmd, "board", "board to resolve the card name within")
	cardRef.bind(cmd, "card", "card name, id, or shortLink")

	return a.newCommand(requires(destructive(cmd), "card"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		obj, boardID, err := a.strictCard(ctx, boardRef, cardRef)
		if err != nil {
			return err
		}
		if a.dryRun {
			return a.plan("cards delete", map[string]string{"card": obj.ID}, nil)
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		if err := client.DeleteCard(ctx, obj.ID); err != nil {
			return err
		}
		// The board id captured during resolution, not re-resolved: retrying
		// a.board here failed whenever the card was given by id with no
		// --board, leaving the deleted card in the index for the whole TTL.
		if boardID != "" {
			a.invalidate(resolve.CardScope(boardID))
		}
		return a.out.Success(deletedView{Kind: "card", ID: obj.ID})
	})
}

// strictCard resolves a card for a destructive command, refusing a prefix. It
// also returns the board id it resolved within, so the caller can invalidate
// that index without resolving again.
func (a *App) strictCard(ctx context.Context, boardRef, cardRef objectRef) (resolve.Object, string, error) {
	if cardRef.id != "" {
		return resolve.Object{ID: cardRef.id}, "", nil
	}
	if cardRef.name == "" {
		return resolve.Object{}, "", errx.Usage("a card is required: pass --card or --card-id")
	}
	if resolve.LooksLikeID(cardRef.name) {
		return resolve.Object{ID: cardRef.name}, "", nil
	}
	r, err := a.strictResolver(ctx)
	if err != nil {
		return resolve.Object{}, "", err
	}
	// The board is resolved strictly too. Refusing a prefix for the card while
	// accepting one for the board still deletes from whichever board the
	// prefix happened to hit — "Prod" matching "Production" is exactly the
	// mistake this command exists to prevent.
	board, err := a.strictBoard(ctx, boardRef, r)
	if err != nil {
		return resolve.Object{}, "", err
	}
	card, err := r.Card(ctx, board.ID, cardRef.name)
	return card, board.ID, err
}

// strictBoard resolves a board without the prefix rung.
func (a *App) strictBoard(ctx context.Context, ref objectRef, r *resolve.Resolver) (resolve.Object, error) {
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
	return r.Board(ctx, query)
}

type deletedView struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (d deletedView) Fields() []output.Field {
	return []output.Field{
		{Name: "deleted", Value: "deleted", Raw: true},
		{Name: "kind", Value: d.Kind, Raw: d.Kind},
		{Name: "id", Value: d.ID, Raw: d.ID},
	}
}

// ---- labels, members, comments, checklists, attachments ----

func (a *App) labelsWriteCommands() []*cobra.Command {
	build := func(use, short, action string, remove bool) *cobra.Command {
		var boardRef, cardRef, labelRef objectRef
		cmd := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs}
		boardRef.bind(cmd, "board", "board holding the label; required unless --label-id is given")
		cardRef.bind(cmd, "card", "card name, id, or shortLink")
		labelRef.bind(cmd, "label", "label name or color")

		return a.newCommand(requires(mutating(cmd), "card", "label"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
			board, err := a.boardFor(ctx, boardRef, cardRef, labelRef)
			if err != nil {
				return err
			}
			card, err := a.card(ctx, board.ID, cardRef)
			if err != nil {
				return err
			}
			r, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			label, err := r.Label(ctx, board.ID, labelRef.nameOrID())
			if err != nil {
				return err
			}
			if a.dryRun {
				return a.plan(action,
					map[string]string{"card": card.ID},
					map[string]string{"label": label.ID})
			}
			client, err := a.trelloClient(ctx)
			if err != nil {
				return err
			}
			if remove {
				err = client.RemoveLabel(ctx, card.ID, label.ID)
			} else {
				err = client.AddLabel(ctx, card.ID, label.ID)
			}
			if err != nil {
				return err
			}
			return a.out.Success(changedView{Action: action, CardID: card.ID, TargetID: label.ID})
		})
	}
	var createBoardRef objectRef
	var labelName, labelColor string
	create := &cobra.Command{
		Use:   "create",
		Short: "Define a new label on a board",
		Long: "Define a new label on a board.\n\n" +
			"This adds to the board's vocabulary. 'labels add' is the other thing —\n" +
			"it puts a label the board already has onto a card.\n\n" +
			"Colours are Trello's, not this tool's, so they are passed through rather\n" +
			"than checked against a list here: the light and dark variants did not\n" +
			"always exist, and a local allowlist would reject colours the API accepts.\n" +
			"Run 'trello-cli labels list' to see what a board already uses.\n\n" +
			"A name that an existing label already has is allowed, because Trello\n" +
			"allows it — but the two become indistinguishable by name afterwards, and\n" +
			"every later --label lookup for that name is an ambiguity you have to\n" +
			"resolve with --label-id.",
		Args: cobra.NoArgs,
	}
	createBoardRef.bind(create, "board", "board name, id, or shortLink")
	create.Flags().StringVar(&labelName, "name", "", "label name (Trello allows an empty one, which can then only be addressed by colour or id)")
	create.Flags().StringVar(&labelColor, "color", "", "label colour, e.g. green, red, sky_light")

	createCmd := a.newCommand(requires(mutating(create), "board", "color"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		if labelColor == "" {
			return errx.Usage("--color is required")
		}
		board, err := a.board(ctx, createBoardRef)
		if err != nil {
			return err
		}
		if a.dryRun {
			return a.plan("labels create",
				map[string]string{"board": board.ID},
				map[string]string{"name": labelName, "color": labelColor})
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		label, err := client.CreateLabel(ctx, board.ID, labelName, labelColor)
		if err != nil {
			return err
		}
		// The board's label set just changed, so a cached index would resolve
		// --label against a vocabulary that no longer matches.
		a.invalidate(resolve.LabelScope(board.ID))
		return a.out.Success(labelView{label})
	})

	var deleteBoardRef, deleteLabelRef objectRef
	del := &cobra.Command{
		Use:   "delete",
		Short: "Remove a label from the board entirely",
		Long: "Remove a label from the board entirely.\n\n" +
			"This is not 'labels remove', which takes a label off one card. This\n" +
			"deletes the label itself, and Trello strips it from every card that\n" +
			"carried it. There is no undo.",
		Args: cobra.NoArgs,
	}
	deleteBoardRef.bind(del, "board", "board name, id, or shortLink")
	deleteLabelRef.bind(del, "label", "label name or color")

	deleteCmd := a.newCommand(requires(destructive(del), "board", "label"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		board, err := a.board(ctx, deleteBoardRef)
		if err != nil {
			return err
		}
		r, err := a.resolver(ctx)
		if err != nil {
			return err
		}
		label, err := r.Label(ctx, board.ID, deleteLabelRef.nameOrID())
		if err != nil {
			return err
		}
		if a.dryRun {
			return a.plan("labels delete", map[string]string{"board": board.ID, "label": label.ID}, nil)
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		if err := client.DeleteLabel(ctx, label.ID); err != nil {
			return err
		}
		a.invalidate(resolve.LabelScope(board.ID))
		return a.out.Success(changedView{Action: "labels delete", TargetID: label.ID})
	})

	return []*cobra.Command{
		createCmd,
		build("add", "Attach a label to a card", "labels add", false),
		build("remove", "Detach a label from a card", "labels remove", true),
		deleteCmd,
	}
}

func (a *App) membersWriteCommands() []*cobra.Command {
	build := func(use, short, action string, remove bool) *cobra.Command {
		var boardRef, cardRef, memberRef objectRef
		cmd := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs}
		boardRef.bind(cmd, "board", "board holding the member; required unless --member-id is given")
		cardRef.bind(cmd, "card", "card name, id, or shortLink")
		memberRef.bind(cmd, "member", "member username or id")

		return a.newCommand(requires(mutating(cmd), "card", "member"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
			board, err := a.boardFor(ctx, boardRef, cardRef, memberRef)
			if err != nil {
				return err
			}
			card, err := a.card(ctx, board.ID, cardRef)
			if err != nil {
				return err
			}
			r, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			member, err := r.Member(ctx, board.ID, memberRef.nameOrID())
			if err != nil {
				return err
			}
			if a.dryRun {
				return a.plan(action,
					map[string]string{"card": card.ID},
					map[string]string{"member": member.ID})
			}
			client, err := a.trelloClient(ctx)
			if err != nil {
				return err
			}
			if remove {
				err = client.UnassignMember(ctx, card.ID, member.ID)
			} else {
				err = client.AssignMember(ctx, card.ID, member.ID)
			}
			if err != nil {
				return err
			}
			return a.out.Success(changedView{Action: action, CardID: card.ID, TargetID: member.ID})
		})
	}
	return []*cobra.Command{
		build("assign", "Assign a member to a card", "members assign", false),
		build("unassign", "Remove a member from a card", "members unassign", true),
	}
}

func (a *App) commentsWriteCommands() []*cobra.Command {
	var boardRef, cardRef objectRef
	var text string
	cmd := &cobra.Command{Use: "add", Short: "Add a comment to a card", Args: cobra.NoArgs}
	boardRef.bind(cmd, "board", "board to resolve the card name within")
	cardRef.bind(cmd, "card", "card name, id, or shortLink")
	cmd.Flags().StringVar(&text, "text", "", "comment body")

	return []*cobra.Command{a.newCommand(requires(mutating(cmd), "card", "text"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		if text == "" {
			return errx.Usage("--text is required")
		}
		obj, err := a.cardAnywhere(ctx, boardRef, cardRef)
		if err != nil {
			return err
		}
		if a.dryRun {
			return a.plan("comments add", map[string]string{"card": obj.ID}, map[string]string{"text": text})
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		comment, err := client.AddComment(ctx, obj.ID, text)
		if err != nil {
			return err
		}
		return a.out.Success(commentView{comment})
	})}
}

func (a *App) checklistsWriteCommands() []*cobra.Command {
	var boardRef, cardRef objectRef
	var name string
	create := &cobra.Command{Use: "create", Short: "Add a checklist to a card", Args: cobra.NoArgs}
	boardRef.bind(create, "board", "board to resolve the card name within")
	cardRef.bind(create, "card", "card name, id, or shortLink")
	create.Flags().StringVar(&name, "name", "", "checklist name")

	createCmd := a.newCommand(requires(mutating(create), "card", "name"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		if name == "" {
			return errx.Usage("--name is required")
		}
		obj, err := a.cardAnywhere(ctx, boardRef, cardRef)
		if err != nil {
			return err
		}
		if a.dryRun {
			return a.plan("checklists create", map[string]string{"card": obj.ID}, map[string]string{"name": name})
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		created, err := client.CreateChecklist(ctx, obj.ID, name)
		if err != nil {
			return err
		}
		return a.out.Success(checklistView{created})
	})

	var checklistID, itemName string
	// No --board or --card here on purpose. They were bound and never read,
	// which is worse than absent: a caller passing them to guard against a
	// checklist id from the wrong card got no guard at all. The checklist id
	// already identifies its card unambiguously.
	addItem := &cobra.Command{Use: "add-item", Short: "Add an item to a checklist", Args: cobra.NoArgs}
	addItem.Flags().StringVar(&checklistID, "checklist-id", "", "checklist id, from 'checklists list'")
	addItem.Flags().StringVar(&itemName, "name", "", "item text")

	addItemCmd := a.newCommand(requires(mutating(addItem), "checklist-id", "name"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		if checklistID == "" || itemName == "" {
			return errx.Usage("--checklist-id and --name are required")
		}
		if a.dryRun {
			return a.plan("checklists add-item",
				map[string]string{"checklist": checklistID},
				map[string]string{"name": itemName})
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		item, err := client.AddCheckItem(ctx, checklistID, itemName, false)
		if err != nil {
			return err
		}
		return a.out.Success(checkItemView{item})
	})

	var toggleBoardRef, toggleCardRef objectRef
	var itemID string
	var undone bool
	toggle := &cobra.Command{Use: "toggle", Short: "Mark a checklist item complete or incomplete", Args: cobra.NoArgs}
	toggleBoardRef.bind(toggle, "board", "board to resolve the card name within")
	toggleCardRef.bind(toggle, "card", "card name, id, or shortLink")
	toggle.Flags().StringVar(&itemID, "item-id", "", "check item id, from 'checklists list'")
	toggle.Flags().BoolVar(&undone, "undone", false, "mark incomplete instead of complete")

	toggleCmd := a.newCommand(requires(mutating(toggle), "card", "item-id"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		if itemID == "" {
			return errx.Usage("--item-id is required")
		}
		obj, err := a.cardAnywhere(ctx, toggleBoardRef, toggleCardRef)
		if err != nil {
			return err
		}
		if a.dryRun {
			return a.plan("checklists toggle",
				map[string]string{"card": obj.ID, "item": itemID},
				map[string]string{"state": stateWord(!undone)})
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		item, err := client.SetCheckItemState(ctx, obj.ID, itemID, !undone)
		if err != nil {
			return err
		}
		return a.out.Success(checkItemView{item})
	})

	return []*cobra.Command{createCmd, addItemCmd, toggleCmd}
}

func (a *App) attachmentsWriteCommands() []*cobra.Command {
	var boardRef, cardRef objectRef
	var attachURL, name string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Attach a URL to a card",
		Long:  "Attach a URL to a card. File uploads are not supported.",
		Args:  cobra.NoArgs,
	}
	boardRef.bind(cmd, "board", "board to resolve the card name within")
	cardRef.bind(cmd, "card", "card name, id, or shortLink")
	cmd.Flags().StringVar(&attachURL, "url", "", "URL to attach")
	cmd.Flags().StringVar(&name, "name", "", "display name for the attachment")

	return []*cobra.Command{a.newCommand(requires(mutating(cmd), "card", "url"), func(ctx context.Context, _ *cobra.Command, _ []string) error {
		if attachURL == "" {
			return errx.Usage("--url is required")
		}
		obj, err := a.cardAnywhere(ctx, boardRef, cardRef)
		if err != nil {
			return err
		}
		if a.dryRun {
			return a.plan("attachments add", map[string]string{"card": obj.ID}, map[string]string{"url": attachURL})
		}
		client, err := a.trelloClient(ctx)
		if err != nil {
			return err
		}
		added, err := client.AddAttachment(ctx, obj.ID, attachURL, name)
		if err != nil {
			return err
		}
		return a.out.Success(attachmentView{added})
	})}
}

// changedView reports a relationship change that returns no useful body.
type changedView struct {
	Action   string `json:"action"`
	CardID   string `json:"cardId"`
	TargetID string `json:"targetId"`
}

func (c changedView) Fields() []output.Field {
	return []output.Field{
		{Name: "action", Value: c.Action, Raw: c.Action},
		{Name: "cardId", Value: c.CardID, Raw: c.CardID},
		{Name: "targetId", Value: c.TargetID, Raw: c.TargetID},
	}
}

type checkItemView struct{ trello.ChecklistItem }

func (i checkItemView) Fields() []output.Field {
	return []output.Field{
		{Name: "id", Value: i.ID, Raw: i.ID},
		{Name: "name", Value: i.Name, Raw: i.Name},
		{Name: "state", Value: i.State, Raw: i.State},
	}
}

// invalidate drops a resolver cache scope after a change that alters it.
func (a *App) invalidate(scope string) {
	if a.res != nil {
		a.res.Invalidate(scope)
	}
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func stateWord(complete bool) string {
	if complete {
		return "complete"
	}
	return "incomplete"
}
