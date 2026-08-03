package cli

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strings"

	"github.com/abigotado/trello-cli/internal/auth"
	"github.com/abigotado/trello-cli/internal/errx"
	"github.com/spf13/cobra"
)

func (a *App) newContractCommand() *cobra.Command {
	return a.newCommand(
		&cobra.Command{
			Use:   "contract",
			Short: "Print the machine contract: envelope version and exit codes",
			Long: "Print the exit-code table and envelope version that every command obeys.\n\n" +
				"This is the authoritative source. docs/contract.md and the shipped skill\n" +
				"reference are generated from it, so an agent can discover the contract at\n" +
				"runtime instead of relying on documentation that may have drifted.",
			Args: cobra.NoArgs,
		},
		func(_ context.Context, _ *cobra.Command, _ []string) error {
			return a.out.Success(errx.Describe())
		},
	)
}

func (a *App) newMeCommand() *cobra.Command {
	return a.newCommand(
		&cobra.Command{
			Use:   "me",
			Short: "Show the authenticated Trello member",
			Args:  cobra.NoArgs,
		},
		func(ctx context.Context, _ *cobra.Command, _ []string) error {
			client, err := a.trelloClient(ctx)
			if err != nil {
				return err
			}
			member, err := client.Me(ctx)
			if err != nil {
				return err
			}
			return a.out.Success(memberView{member})
		},
	)
}

func (a *App) newAuthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage Trello credentials, across any number of accounts",
		Long: "Manage Trello credentials.\n\n" +
			"Accounts are selected per invocation with --account or TRELLO_CLI_ACCOUNT.\n" +
			"There is deliberately no command to switch the active account: that would\n" +
			"be hidden global state, and two concurrent runs would race over it.",
		Args: usageArgs(cobra.NoArgs),
		// Not runnable on its own; see group() in read.go for why this is a
		// usage error rather than help printed with exit 0.
		RunE: func(c *cobra.Command, _ []string) error {
			return errx.Usage("%s needs a subcommand", c.CommandPath())
		},
	}
	cmd.AddCommand(
		a.newAuthLoginCommand(),
		a.newAuthStatusCommand(),
		a.newAuthListCommand(),
		a.newAuthDefaultCommand(),
		a.newAuthLogoutCommand(),
	)
	return cmd
}

// targetAccount returns the account a management command should act on.
//
// Management commands take the name from --account and fall back to the
// conventional default, rather than going through credential resolution: they
// operate on the store itself, not on whatever happens to be authenticated.
func (a *App) targetAccount() (string, error) {
	name := a.account
	if name == "" {
		name = auth.DefaultAccount
	}
	if err := auth.ValidateAccountName(name); err != nil {
		return "", err
	}
	return name, nil
}

func (a *App) newAuthLoginCommand() *cobra.Command {
	var apiKey, token string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store an API key and token for an account, from flags or stdin",
		Long: "Store credentials for an account in the OS keychain.\n\n" +
			"Get an API key at https://trello.com/power-ups/admin and a token by\n" +
			"authorizing it. Use --account to name the account; without one it is\n" +
			"stored as \"default\". TRELLO_API_KEY and TRELLO_TOKEN still take precedence\n" +
			"over anything stored here, which is the right choice for CI and headless\n" +
			"agents.\n\n" +
			"Prefer stdin to the flags. A credential passed as --token is visible in\n" +
			"the shell history and, while the process runs, to anyone who can read\n" +
			"'ps' as you. With neither flag given, this reads two lines from stdin:\n" +
			"the API key, then the token. Let the shell keep them off the screen:\n\n" +
			"  read -rs 'key?API key: '; echo\n" +
			"  read -rs 'tok?Token: '; echo\n" +
			"  printf '%s\\n%s\\n' \"$key\" \"$tok\" | trello-cli auth login --account work\n" +
			"  unset key tok",
		// Not cobra.NoArgs: its message embeds the offending argument, and a
		// positional is exactly what someone reaches for first on a login
		// command. `auth login MY-TOKEN` would then echo the credential into
		// the error envelope on stdout and any log that captures it.
		Args: func(*cobra.Command, []string) error { return nil },
		Annotations: map[string]string{
			// login writes the same stored credential logout removes, so both
			// must answer to TRELLO_CLI_READONLY.
			annotationMutates: "true",
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Trello API key")
	cmd.Flags().StringVar(&token, "token", "", "Trello API token")

	return a.newCommand(requires(cmd, "api-key", "token"), func(ctx context.Context, _ *cobra.Command, args []string) error {
		// Deliberately does not quote or echo the argument: it is very likely
		// to be the token itself.
		if len(args) > 0 {
			return errx.Usage("auth login takes no positional arguments; pass credentials with --api-key and --token")
		}
		switch {
		case apiKey == "" && token == "":
			// Both absent is the stdin path. Partially absent is not: mixing
			// one flag with one piped line is where a mistake would put the
			// wrong secret in the wrong field, silently.
			var err error
			if apiKey, token, err = readCredentials(a.stdin); err != nil {
				return err
			}
		case apiKey == "" || token == "":
			return errx.Usage("pass both --api-key and --token, or neither and supply them on stdin")
		}
		name, err := a.targetAccount()
		if err != nil {
			return err
		}
		creds := auth.Credentials{APIKey: apiKey, Token: token}
		view := accountView{
			Account:       name,
			Authenticated: true,
			Source:        auth.SourceKeyring,
			APIKeySuffix:  keySuffix(creds.APIKey),
			Fingerprint:   creds.Fingerprint(),
		}
		if a.dryRun {
			// planView, not accountView: a dry run that returned the success
			// shape with "authenticated": true would be indistinguishable on
			// stdout from a credential write that actually happened.
			return a.plan("auth login", map[string]string{"account": name}, nil)
		}
		if err := a.store.Save(ctx, name, creds); err != nil {
			return err
		}
		// Registered after the credential lands, so a failed write never
		// leaves a name pointing at nothing.
		if err := a.registry.Add(name); err != nil {
			return err
		}
		view.Default = a.registry.Default() == name
		return a.out.Success(view)
	})
}

func (a *App) newAuthStatusCommand() *cobra.Command {
	return a.newCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Report which credentials this invocation would use",
			Args:  cobra.NoArgs,
		},
		func(ctx context.Context, _ *cobra.Command, _ []string) error {
			res, err := a.resolveCredentials(ctx)
			if err != nil {
				// Not being authenticated is the honest answer to `status`,
				// not a failure of the command. A keychain that is present but
				// unreadable still is a failure, so only these reasons pass.
				var typed *errx.Error
				if errors.As(err, &typed) && (typed.Reason == "NOT_AUTHENTICATED" || typed.Reason == "UNKNOWN_ACCOUNT") {
					return a.out.Success(accountView{Source: auth.SourceNone})
				}
				return err
			}
			return a.out.Success(accountView{
				Account:       res.Account,
				Authenticated: true,
				Source:        res.Source,
				APIKeySuffix:  keySuffix(res.Credentials.APIKey),
				Fingerprint:   res.Credentials.Fingerprint(),
				Default:       res.Account != "" && a.registry.Default() == res.Account,
			})
		},
	)
}

func (a *App) newAuthListCommand() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the stored accounts",
		Long: "List the stored accounts.\n\n" +
			"Names and the default come from the local registry, so this reads no\n" +
			"credentials. Pass --check to also verify each account still has a usable\n" +
			"credential — that reads the keychain once per account, which on macOS can\n" +
			"raise one access prompt per account for an unsigned binary.",
		Args: cobra.NoArgs,
	}
	cmd.Flags().BoolVar(&check, "check", false, "verify each account's credential (reads the keychain per account)")

	return a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		names := a.registry.List()
		views := make([]accountView, 0, len(names))
		for _, name := range names {
			view := accountView{Account: name, Default: a.registry.Default() == name}
			// Deliberately not read by default. Loading every account meant one
			// keychain access per account, and on macOS an unsigned binary
			// raises a modal prompt for each — so an agent calling this to
			// discover accounts would hang on the first invisible dialog.
			if check {
				if creds, err := a.store.Load(ctx, name); err == nil && creds.Valid() {
					view.Authenticated = true
					view.Source = auth.SourceKeyring
					view.APIKeySuffix = keySuffix(creds.APIKey)
					view.Fingerprint = creds.Fingerprint()
				}
			}
			views = append(views, view)
		}
		return a.out.Success(views)
	})
}

func (a *App) newAuthDefaultCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "default <account>",
		Short: "Set the account used when none is named",
		Args:  cobra.ExactArgs(1),
		Annotations: map[string]string{
			annotationMutates: "true",
		},
	}
	return a.newCommand(cmd, func(_ context.Context, _ *cobra.Command, args []string) error {
		name := args[0]
		// --account means "the account this invocation acts on" everywhere
		// else, so silently ignoring it here would be the one inconsistency in
		// the surface. Refuse rather than pick.
		if a.account != "" && a.account != name {
			return errx.Usage("auth default takes the account as its argument; --account %s contradicts it", a.account)
		}
		if err := auth.ValidateAccountName(name); err != nil {
			return err
		}
		if a.dryRun {
			return a.plan("auth default", map[string]string{"account": name}, nil)
		}
		if err := a.registry.SetDefault(name); err != nil {
			return err
		}
		return a.out.Success(accountView{Account: name, Default: true})
	})
}

func (a *App) newAuthLogoutCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove one account's stored credentials",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			annotationMutates: "true",
		},
	}
	return a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		name, err := a.targetAccount()
		if err != nil {
			return err
		}
		if a.dryRun {
			return a.plan("auth logout", map[string]string{"account": name}, nil)
		}
		if err := a.store.Delete(ctx, name); err != nil {
			return err
		}
		if err := a.registry.Remove(name); err != nil {
			return err
		}
		return a.out.Success(accountView{Account: name, Source: auth.SourceNone})
	})
}

// readCredentials takes the API key and the token from stdin, in that order,
// one per line.
//
// Passing a credential as a flag puts it in the shell history and, for as long
// as the process lives, in the argv that `ps` prints for anyone running as the
// same user — verified, not assumed. stdin has neither problem.
//
// Echo is deliberately left to the shell rather than handled here. `read -rs`
// already does it on every platform this ships to, and doing it in Go would
// mean a terminal dependency for a job the caller's shell does better.
func readCredentials(stdin *os.File) (string, string, error) {
	if stdin == nil {
		return "", "", errx.Usage("no stdin to read credentials from")
	}
	// A terminal here means the caller typed `auth login` bare and is now
	// staring at a cursor with no prompt, wondering if it hung. Say what to do
	// instead of silently blocking on a read.
	if info, err := stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		return "", "", &errx.Error{
			Code:    errx.CodeUsage,
			Reason:  "USAGE",
			Message: "no credentials given and stdin is a terminal",
			Hint:    "pipe the key and the token as two lines, or pass --api-key and --token; see 'trello-cli auth login --help'",
		}
	}

	scanner := bufio.NewScanner(stdin)
	// A Trello token is 64 hex characters and a key is 32, so the default 64KB
	// buffer is ample; a line longer than that is not a credential.
	fields := make([]string, 0, 2)
	for len(fields) < 2 && scanner.Scan() {
		fields = append(fields, strings.TrimSpace(scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		return "", "", errx.Usage("read credentials from stdin: %v", err)
	}
	if len(fields) < 2 || fields[0] == "" || fields[1] == "" {
		// Deliberately does not echo what was read: the first line is very
		// likely to be a real key even when the second is missing.
		return "", "", errx.Usage("stdin must hold two non-empty lines: the API key, then the token")
	}
	return fields[0], fields[1], nil
}
