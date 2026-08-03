package cli

import (
	"context"

	"github.com/abigotado/trello-cli/internal/errx"
	"github.com/abigotado/trello-cli/internal/output"
	"github.com/abigotado/trello-cli/internal/skills"
	"github.com/spf13/cobra"
)

func (a *App) newSkillsCommand() *cobra.Command {
	return group("skills", "Install the agent skill into the tools that read it",
		a.newSkillsInstallCommand(),
		a.newSkillsUninstallCommand(),
	)
}

// providerFlags are shared by install and uninstall.
type skillFlags struct {
	provider string
	scope    string
	dest     string
}

func (f *skillFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.provider, "provider", "", "claude, codex, cursor, or all")
	cmd.Flags().StringVar(&f.scope, "scope", "user", "user or project")
	cmd.Flags().StringVar(&f.dest, "dest", "", "explicit skills root, overriding the provider's own location")
}

// targets expands --provider into the providers to act on.
func (f *skillFlags) targets() ([]skills.Provider, error) {
	if f.provider == "" || f.provider == "all" {
		if f.dest != "" {
			// One --dest applied to three providers is how an install lands
			// three copies in one directory and each overwrites the last.
			return nil, errx.Usage("--dest cannot be combined with every provider; name one with --provider")
		}
		return skills.Providers(), nil
	}
	p, err := skills.ParseProvider(f.provider)
	if err != nil {
		return nil, err
	}
	return []skills.Provider{p}, nil
}

func (f *skillFlags) options(a *App, provider skills.Provider) (skills.Options, error) {
	scope, err := skills.ParseScope(f.scope)
	if err != nil {
		return skills.Options{}, err
	}
	projectDir := ""
	if scope == skills.ScopeProject {
		projectDir = "."
	}
	return skills.Options{
		Provider:   provider,
		Scope:      scope,
		ProjectDir: projectDir,
		Dest:       f.dest,
		Confirmed:  a.assumeYes,
		DryRun:     a.dryRun,
		LookupEnv:  a.lookupEnv,
	}, nil
}

func (a *App) newSkillsInstallCommand() *cobra.Command {
	var flags skillFlags
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the trello skill so an agent can discover this tool",
		Long: "Install the trello skill into the directories Claude Code, Codex, and Cursor\n" +
			"read.\n\n" +
			"Re-running is safe: files this tool wrote are refreshed in place, and the\n" +
			"command refuses to touch a file it did not write unless you pass --yes.\n" +
			"--dry-run reports exactly what would change and writes nothing at all.\n\n" +
			"This writes only to the local filesystem, so TRELLO_CLI_READONLY does not\n" +
			"gate it — that variable locks Trello, not this machine.",
		Args: cobra.NoArgs,
	}
	flags.bind(cmd)

	return a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		targets, err := flags.targets()
		if err != nil {
			return err
		}
		results := make([]skillResultView, 0, len(targets))
		for _, provider := range targets {
			opts, optErr := flags.options(a, provider)
			if optErr != nil {
				return optErr
			}
			res, installErr := skills.Install(ctx, opts)
			if installErr != nil {
				return installErr
			}
			results = append(results, skillResultView{res})
		}
		return a.out.Success(results)
	})
}

func (a *App) newSkillsUninstallCommand() *cobra.Command {
	var flags skillFlags
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the trello skill files this tool installed",
		Long: "Remove the trello skill.\n\n" +
			"Only files this tool wrote and that still match what it wrote are removed.\n" +
			"Anything edited since is reported and left alone.",
		Args: cobra.NoArgs,
	}
	flags.bind(cmd)

	return a.newCommand(cmd, func(ctx context.Context, _ *cobra.Command, _ []string) error {
		targets, err := flags.targets()
		if err != nil {
			return err
		}
		results := make([]skillResultView, 0, len(targets))
		for _, provider := range targets {
			opts, optErr := flags.options(a, provider)
			if optErr != nil {
				return optErr
			}
			res, uninstallErr := skills.Uninstall(ctx, opts)
			if uninstallErr != nil {
				return uninstallErr
			}
			results = append(results, skillResultView{res})
		}
		return a.out.Success(results)
	})
}

// skillResultView renders one provider's install outcome.
type skillResultView struct{ skills.Result }

func (s skillResultView) Fields() []output.Field {
	changed := 0
	for _, f := range s.Files {
		if f.Applied {
			changed++
		}
	}
	state := "changed"
	if s.InSync && changed == 0 {
		state = "up to date"
	}
	if s.DryRun {
		state = "dry-run"
	}
	return []output.Field{
		{Name: "provider", Value: s.Provider, Raw: s.Provider},
		{Name: "state", Value: state, Raw: state},
		{Name: "root", Value: s.Root, Raw: s.Root},
		{Name: "inSync", Value: "", Raw: s.InSync},
		{Name: "dryRun", Value: "", Raw: s.DryRun},
		{Name: "files", Value: "", Raw: s.Files},
	}
}
