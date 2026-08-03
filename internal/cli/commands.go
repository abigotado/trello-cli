package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/abigotado-niko/trello-cli/internal/auth"
	"github.com/abigotado-niko/trello-cli/internal/errx"
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
		Short: "Manage Trello credentials",
		Args:  usageArgs(cobra.NoArgs),
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(a.newAuthLoginCommand(), a.newAuthStatusCommand(), a.newAuthLogoutCommand())
	return cmd
}

func (a *App) newAuthLoginCommand() *cobra.Command {
	var apiKey, token string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store a Trello API key and token in the OS keychain",
		Long: "Store credentials in the OS keychain.\n\n" +
			"Get an API key at https://trello.com/power-ups/admin and a token by\n" +
			"authorizing it. TRELLO_API_KEY and TRELLO_TOKEN always take precedence over\n" +
			"anything stored here, and are the right choice for CI and headless agents.",
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Trello API key")
	cmd.Flags().StringVar(&token, "token", "", "Trello API token")

	return a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		if apiKey == "" || token == "" {
			return errx.Usage("both --api-key and --token are required")
		}
		creds := auth.Credentials{APIKey: apiKey, Token: token}
		if a.dryRun {
			return a.out.Success(authStatusView{
				Authenticated: true,
				Source:        auth.SourceKeyring,
				APIKeySuffix:  keySuffix(creds.APIKey),
				Fingerprint:   creds.Fingerprint(),
			})
		}
		if err := a.store.Save(ctx, creds); err != nil {
			return err
		}
		return a.out.Success(authStatusView{
			Authenticated: true,
			Source:        auth.SourceKeyring,
			APIKeySuffix:  keySuffix(creds.APIKey),
			Fingerprint:   creds.Fingerprint(),
		})
	})
}

func (a *App) newAuthStatusCommand() *cobra.Command {
	return a.newCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Report whether credentials are configured and where they came from",
			Args:  cobra.NoArgs,
		},
		func(ctx context.Context, _ *cobra.Command, _ []string) error {
			creds, source, err := auth.Resolver{Lookup: a.lookupEnv, Store: a.store}.Resolve(ctx)
			if err != nil {
				// Not being authenticated is the honest answer to `status`,
				// not a failure of the command. A keychain that is present but
				// unreadable still is a failure, so only the one reason passes.
				var typed *errx.Error
				if errors.As(err, &typed) && typed.Reason == "NOT_AUTHENTICATED" {
					return a.out.Success(authStatusView{Source: auth.SourceNone})
				}
				return err
			}
			return a.out.Success(authStatusView{
				Authenticated: true,
				Source:        source,
				APIKeySuffix:  keySuffix(creds.APIKey),
				Fingerprint:   creds.Fingerprint(),
			})
		},
	)
}

func (a *App) newAuthLogoutCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove stored credentials from the OS keychain",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			annotationMutates: "true",
		},
	}
	return a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		if a.dryRun {
			fmt.Fprintln(a.stderr, "dry-run: would delete the stored credentials")
			return a.out.Success(authStatusView{Source: auth.SourceNone})
		}
		if err := a.store.Delete(ctx); err != nil {
			return err
		}
		return a.out.Success(authStatusView{Source: auth.SourceNone})
	})
}
