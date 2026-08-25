package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"
)

func TestBuildVersionUsesReleaseFallbackOnlyWithoutModuleVersion(t *testing.T) {
	tests := []struct {
		name     string
		fallback string
		info     *debug.BuildInfo
		ok       bool
		want     versionView
	}{
		{
			name:     "missing build info uses release fallback",
			fallback: "v9.8.7",
			want:     versionView{Version: "v9.8.7"},
		},
		{
			name:     "empty module version uses release fallback",
			fallback: "v9.8.7",
			info:     &debug.BuildInfo{Main: debug.Module{}},
			ok:       true,
			want:     versionView{Version: "v9.8.7"},
		},
		{
			name:     "development module version uses release fallback",
			fallback: "v9.8.7",
			info:     &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			ok:       true,
			want:     versionView{Version: "v9.8.7"},
		},
		{
			name:     "module version overrides release fallback",
			fallback: "v9.8.7",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "v1.2.3"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abc123"},
					{Key: "vcs.time", Value: "2026-08-25T00:00:00Z"},
				},
			},
			ok: true,
			want: versionView{
				Version:    "v1.2.3",
				Commit:     "abc123",
				CommitTime: "2026-08-25T00:00:00Z",
			},
		},
		{
			name: "empty fallback uses development version",
			want: versionView{Version: devVersion},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildVersion(func() (*debug.BuildInfo, bool) {
				return tt.info, tt.ok
			}, tt.fallback)
			if got.Version != tt.want.Version {
				t.Errorf("version = %q, want %q", got.Version, tt.want.Version)
			}
			if got.Commit != tt.want.Commit {
				t.Errorf("commit = %q, want %q", got.Commit, tt.want.Commit)
			}
			if got.CommitTime != tt.want.CommitTime {
				t.Errorf("commitTime = %q, want %q", got.CommitTime, tt.want.CommitTime)
			}
		})
	}
}

func TestBuiltSourceDistributionReportsInjectedReleaseVersion(t *testing.T) {
	tempDir := t.TempDir()
	binaryName := "trello-cli"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(tempDir, binaryName)

	build := exec.Command(
		"go", "build",
		"-buildvcs=false",
		"-trimpath",
		"-ldflags=-X github.com/abigotado/trello-cli/internal/cli.releaseVersion=v9.8.7",
		"-o", binaryPath,
		"./cmd/trello-cli",
	)
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build source distribution: %v\n%s", err, output)
	}

	home := filepath.Join(tempDir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatalf("create isolated home: %v", err)
	}
	command := exec.Command(binaryPath, "version", "-o", "json")
	command.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"TRELLO_API_KEY=",
		"TRELLO_TOKEN=",
	)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("run built version command: %v", err)
	}

	var envelope struct {
		OK   bool        `json:"ok"`
		Data versionView `json:"data"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode version envelope: %v", err)
	}
	if !envelope.OK {
		t.Fatal("version command returned a failure envelope")
	}
	if envelope.Data.Version != "v9.8.7" {
		t.Errorf("version = %q, want %q", envelope.Data.Version, "v9.8.7")
	}
	if envelope.Data.Commit != "" || envelope.Data.CommitTime != "" {
		t.Errorf("VCS fields = (%q, %q), want empty", envelope.Data.Commit, envelope.Data.CommitTime)
	}
}
