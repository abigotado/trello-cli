package cli

import (
	"context"
	"runtime"
	"runtime/debug"

	"github.com/abigotado/trello-cli/internal/output"
	"github.com/spf13/cobra"
)

// devVersion is what a build carrying no version reports.
//
// The Go toolchain writes "(devel)" for `go run` and for a build made outside a
// version-control checkout. That spelling is a toolchain implementation detail
// with parentheses in it; the contract reports a plain token instead.
const devVersion = "devel"

func (a *App) newVersionCommand() *cobra.Command {
	return a.newCommand(
		&cobra.Command{
			Use:   "version",
			Short: "Print the build this binary was produced from",
			Long: "Print the version, commit, and toolchain of this binary.\n\n" +
				"Everything here is read from the build information the Go toolchain\n" +
				"embeds. There are no ldflags: a value injected at link time would\n" +
				"only ever reach a binary the release pipeline built, never one a\n" +
				"user installed with 'go install'.\n\n" +
				"The version is always reported. Commit and commitTime are empty\n" +
				"for a 'go install' build, which compiles the module zip the proxy\n" +
				"serves and so has no VCS history to read; quote the version when\n" +
				"reporting a bug against one. A binary from a release archive or a\n" +
				"local checkout carries both, and one built from a modified tree\n" +
				"reports a version ending in '+dirty'.\n\n" +
				"This answers a different question from 'trello-cli contract'. The\n" +
				"contract describes the output shape, which many builds share; this\n" +
				"names one build.",
			Args: cobra.NoArgs,
		},
		func(_ context.Context, _ *cobra.Command, _ []string) error {
			return a.out.Success(buildVersion(debug.ReadBuildInfo))
		},
	)
}

// buildVersion reads the embedded build information.
//
// read is a parameter so a test can supply build information rather than
// depending on how the test binary itself happened to be built.
func buildVersion(read func() (*debug.BuildInfo, bool)) versionView {
	view := versionView{
		Version: devVersion,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
	info, ok := read()
	if !ok {
		return view
	}
	if info.GoVersion != "" {
		view.Go = info.GoVersion
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		view.Version = v
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			view.Commit = setting.Value
		case "vcs.time":
			view.CommitTime = setting.Value
		}
	}
	return view
}

// versionView renders the build stamp.
//
// No omitempty on any tag: -o raw marshals this struct directly while -o json
// goes through Fields(), and omitempty would make raw drop keys that json
// keeps. A machine contract with a conditional key set is worse than one with
// empty strings in it.
type versionView struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	CommitTime string `json:"commitTime"`
	Go         string `json:"go"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
}

func (v versionView) Fields() []output.Field {
	return []output.Field{
		{Name: "version", Value: v.Version, Raw: v.Version},
		{Name: "commit", Value: v.Commit, Raw: v.Commit},
		{Name: "commitTime", Value: v.CommitTime, Raw: v.CommitTime},
		{Name: "go", Value: v.Go, Raw: v.Go},
		{Name: "os", Value: v.OS, Raw: v.OS},
		{Name: "arch", Value: v.Arch, Raw: v.Arch},
	}
}
