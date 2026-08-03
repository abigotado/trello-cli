// Package cli wires the cobra command tree.
//
// It contains no business logic and never calls net/http: a command that needs
// a request the client does not expose gets the method added to
// internal/trello instead.
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/abigotado/trello-cli/internal/auth"
	"github.com/abigotado/trello-cli/internal/config"
	"github.com/abigotado/trello-cli/internal/errx"
	"github.com/abigotado/trello-cli/internal/output"
	"github.com/abigotado/trello-cli/internal/resolve"
	"github.com/abigotado/trello-cli/internal/trello"
	"github.com/spf13/cobra"
)

// Annotation keys read by the middleware in newCommand.
const (
	// annotationMutates marks a command that changes remote state. It gates
	// --dry-run and TRELLO_CLI_READONLY.
	annotationMutates = "mutates"
	// annotationDestructive marks a command whose effect cannot be undone from
	// this tool. It additionally requires --yes.
	annotationDestructive = "destructive"
)

// App is the per-invocation state shared by every command.
type App struct {
	cfg    config.Config
	out    *output.Writer
	log    *slog.Logger
	client *trello.Client
	res    *resolve.Resolver
	// token is kept only to key the resolver's on-disk index. It is never
	// logged, rendered, or written anywhere but that hash.
	token string

	// Resolved persistent flags.
	format    string
	fields    []string
	dryRun    bool
	assumeYes bool
	timeout   time.Duration
	verbose   bool
	jsonAlias bool
	fuzzy     bool
	noCache   bool

	// cancels holds the context cancels to run when Execute returns. Kept on
	// the App rather than at package scope so two Apps in one test process
	// cannot cancel each other's contexts.
	cancels []context.CancelFunc

	// Injected for tests.
	lookupEnv func(string) (string, bool)
	store     auth.Store
	stdout    *os.File
	stderr    *os.File
}

// NewApp builds an App with production defaults.
func NewApp() *App {
	return &App{
		lookupEnv: os.LookupEnv,
		store:     auth.KeyringStore{},
		stdout:    os.Stdout,
		stderr:    os.Stderr,
	}
}

// NewRootCommand assembles the command tree.
func (a *App) NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "trello-cli",
		Short: "Manage Trello from the command line and from AI agents",
		Long: "trello-cli manages Trello boards, lists, and cards.\n\n" +
			"Output is a stable JSON envelope with documented exit codes, so it can be\n" +
			"driven by an AI agent without an LLM in the parsing loop. Run\n" +
			"'trello-cli contract' for the machine contract.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Cobra's default root validator rejects an unknown subcommand with a
		// plain error, which would exit 1. usageArgs re-types it as a usage
		// error so a mistyped command reads as the caller's mistake.
		Args: usageArgs(func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return errx.Usage("unknown command %q for %q", args[0], cmd.CommandPath())
			}
			return nil
		}),
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}

	flags := root.PersistentFlags()
	flags.StringVarP(&a.format, "output", "o", "", "output format: text, json, or raw (default json when stdout is not a terminal)")
	flags.StringSliceVar(&a.fields, "fields", nil, "comma-separated allowlist of fields to emit")
	flags.BoolVar(&a.dryRun, "dry-run", false, "resolve everything and print the intended change without making it")
	flags.BoolVar(&a.assumeYes, "yes", false, "confirm a destructive operation")
	flags.DurationVar(&a.timeout, "timeout", 0, "abort the command after this duration (default 30s)")
	flags.BoolVarP(&a.verbose, "verbose", "v", false, "log request activity to stderr")
	flags.BoolVar(&a.fuzzy, "fuzzy", false, "allow substring name matching (off by default: it fails by confidently picking the wrong object)")
	flags.BoolVar(&a.noCache, "no-cache", false, "bypass the local name-resolution index")
	// Retained so existing callers keep working after the move to -o.
	flags.BoolVar(&a.jsonAlias, "json", false, "alias for --output json")
	_ = flags.MarkHidden("json")

	// Cobra reports a flag-parse failure as a bare error and would exit 1,
	// which the contract reserves for internal defects.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return errx.Usage("%v", err)
	})

	root.PersistentPreRunE = a.setup

	root.AddCommand(
		a.newContractCommand(),
		a.newMeCommand(),
		a.newAuthCommand(),
	)
	root.AddCommand(a.readCommands()...)
	return root
}

// setup resolves configuration and output before any command body runs.
func (a *App) setup(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(a.lookupEnv)
	if err != nil {
		return err
	}
	if a.timeout > 0 {
		cfg.Timeout = a.timeout
	}
	a.cfg = cfg

	format := output.DefaultFormat(a.stdout)
	if a.jsonAlias {
		format = output.FormatJSON
	}
	if a.format != "" {
		if format, err = output.ParseFormat(a.format); err != nil {
			return err
		}
	}
	a.out = &output.Writer{Format: format, Fields: a.fields, Out: a.stdout, Err: a.stderr}

	level := slog.LevelWarn
	if a.verbose {
		level = slog.LevelDebug
	}
	// Logs go to stderr unconditionally. stdout carries only the envelope, and
	// a single stray line there corrupts every downstream parse.
	a.log = slog.New(slog.NewTextHandler(a.stderr, &slog.HandlerOptions{Level: level}))

	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeout)
	cmd.SetContext(ctx)
	a.cancels = append(a.cancels, cancel)
	return nil
}

// trelloClient resolves credentials and builds the API client on demand.
//
// It is lazy so that commands which need no credentials — contract, auth
// login, help — never touch the keychain.
func (a *App) trelloClient(ctx context.Context) (*trello.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	creds, _, err := auth.Resolver{Lookup: a.lookupEnv, Store: a.store}.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	a.token = creds.Token
	a.client = trello.New(
		a.cfg.BaseURL,
		trello.Credentials{APIKey: creds.APIKey, Token: creds.Token},
		a.cfg.Concurrency,
		trello.WithLogger(a.log),
	)
	return a.client, nil
}

// newCommand builds a command with the shared rails already attached.
//
// Every command must be built through this. One assembled by hand silently
// opts out of the read-only gate and the confirmation gate, and the omission is
// invisible until someone deletes the wrong card.
//
// --dry-run is deliberately not enforced here. What a dry run should print is
// specific to each command — which names resolved to which ids — so the
// middleware cannot produce it. It gates the mutating call inside each command
// body instead, and the tests for those commands are what hold that line.
func (a *App) newCommand(cmd *cobra.Command, run func(context.Context, *cobra.Command, []string) error) *cobra.Command {
	cmd.Args = usageArgs(cmd.Args)
	inner := run
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if a.isMutating(c) {
			if a.cfg.ReadOnly {
				return &errx.Error{
					Code:    errx.CodeUsage,
					Reason:  "READ_ONLY",
					Message: "TRELLO_CLI_READONLY is set, so mutating commands are disabled",
					Hint:    "unset TRELLO_CLI_READONLY to allow changes",
				}
			}
			if a.isDestructive(c) && !a.assumeYes && !a.dryRun {
				return errx.ConfirmRequired(c.CommandPath())
			}
		}
		return inner(c.Context(), c, args)
	}
	return cmd
}

// usageArgs converts cobra's positional-argument errors into contract errors.
//
// Cobra validates arguments before RunE and returns a plain error, which would
// reach the top level untyped and be reported as CodeInternal — telling an
// agent a mistyped command is a bug in this tool rather than in its own call.
// Wrapping the validator is the only place that catches every command,
// including the default "unknown command" check on the root.
func usageArgs(validator cobra.PositionalArgs) cobra.PositionalArgs {
	if validator == nil {
		validator = cobra.ArbitraryArgs
	}
	return func(cmd *cobra.Command, args []string) error {
		if err := validator(cmd, args); err != nil {
			return errx.Usage("%v", err)
		}
		return nil
	}
}

func (a *App) isMutating(cmd *cobra.Command) bool {
	return cmd.Annotations[annotationMutates] == "true"
}

func (a *App) isDestructive(cmd *cobra.Command) bool {
	return cmd.Annotations[annotationDestructive] == "true"
}

// Execute runs the command tree and returns the process exit code.
func Execute(args []string) errx.Code {
	app := NewApp()
	root := app.NewRootCommand()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx, root, args)
}

// Run executes root and converts its outcome into an exit code.
//
// The recover here is what keeps the contract honest: the Go runtime
// terminates a panicking binary with status 2, which the contract assigns to a
// usage error, so an agent would read a nil dereference as "you called it
// wrong" and retry forever. Mapping panics to CodeInternal tells it to stop.
func (a *App) Run(ctx context.Context, root *cobra.Command, args []string) (code errx.Code) {
	root.SetArgs(args)
	defer func() {
		for _, cancel := range a.cancels {
			cancel()
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(a.stderr, "internal error: %v\n\n%s\n", r, debug.Stack())
			code = errx.CodeInternal
		}
	}()

	err := root.ExecuteContext(ctx)
	if err == nil {
		return errx.CodeOK
	}
	// Single boundary where an error becomes an exit code. Context errors
	// arrive here untyped from any layer that returned ctx.Err() directly, and
	// without translation a timeout would be reported as an internal defect
	// the caller must not retry.
	err = errx.Translate(err)
	// setup may have failed before a writer existed.
	if a.out == nil {
		a.out = &output.Writer{Format: output.DefaultFormat(a.stdout), Out: a.stdout, Err: a.stderr}
	}
	return a.out.Failure(err)
}
