package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abigotado-niko/trello-cli/internal/errx"
)

// The generated files are the copies an agent actually reads. If regenerating
// would change them, the committed contract no longer matches internal/errx —
// which is exactly the drift the generator exists to prevent.
func TestGeneratedContractIsUpToDate(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() error = %v", err)
	}
	want := renderMarkdown(errx.Describe())

	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			path := filepath.Join(root, target)
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s is missing; run `go generate ./...`: %v", target, err)
			}
			if string(got) != want {
				t.Errorf("%s is out of date; run `go generate ./...`", target)
			}
		})
	}
}

func TestRenderIncludesEveryCode(t *testing.T) {
	out := renderMarkdown(errx.Describe())
	for _, info := range errx.Codes() {
		if !strings.Contains(out, "`"+info.Name+"`") {
			t.Errorf("rendered contract omits code %d (%s)", info.Code, info.Name)
		}
		if !strings.Contains(out, info.NextMove) {
			t.Errorf("rendered contract omits the next move for %s", info.Name)
		}
	}
	if !strings.Contains(out, generatedNotice) {
		t.Error("rendered contract lacks the do-not-edit notice")
	}
}
