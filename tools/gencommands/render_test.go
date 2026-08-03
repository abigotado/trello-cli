package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The generated files are what an agent actually reads. If regenerating would
// change them, the committed reference no longer matches the binary — which is
// the whole drift this generator exists to prevent.
func TestGeneratedCommandsAreUpToDate(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() error = %v", err)
	}
	globals, leaves := collect(rootCommand())
	want := renderMarkdown(globals, leaves)

	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			got, err := os.ReadFile(filepath.Join(root, target))
			if err != nil {
				t.Fatalf("%s is missing; run `go generate ./...`: %v", target, err)
			}
			if string(got) != want {
				t.Errorf("%s is out of date; run `go generate ./...`", target)
			}
		})
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	// Annotations live in a map, and ranging a map is randomised. Rendering
	// twice from two freshly built trees is what catches a map range that
	// leaked into the output.
	first := renderFromTree()
	for i := 0; i < 5; i++ {
		if renderFromTree() != first {
			t.Fatal("rendering is not deterministic")
		}
	}
}

func renderFromTree() string {
	globals, leaves := collect(rootCommand())
	return renderMarkdown(globals, leaves)
}

// help and completion are injected by cobra at run time, not declared by this
// tool, so they must never appear in a reference describing its surface.
func TestGeneratedReferenceExcludesCobraBuiltins(t *testing.T) {
	out := renderFromTree()
	for _, builtin := range []string{"trello-cli help", "trello-cli completion"} {
		if strings.Contains(out, "### `"+builtin) {
			t.Errorf("the reference documents the cobra builtin %q", builtin)
		}
	}
}

// A blanket -id suffix filter would delete these two, which have no name twin,
// making both commands uncallable from the reference.
func TestIdOnlyFlagsSurviveTwinSuppression(t *testing.T) {
	out := renderFromTree()
	for _, flag := range []string{"--checklist-id", "--item-id"} {
		if !strings.Contains(out, flag) {
			t.Errorf("%s was suppressed but has no name twin", flag)
		}
	}
	// While a flag that does have a twin is suppressed.
	if strings.Contains(out, "--board-id") {
		t.Error("--board-id has a --board twin and should be suppressed")
	}
}

// The gate is what an agent needs to know before calling a write command.
func TestGatesAreRendered(t *testing.T) {
	out := renderFromTree()
	if !strings.Contains(out, "### `trello-cli cards delete`  **W D**") {
		t.Error("cards delete is not marked destructive")
	}
	if !strings.Contains(out, "### `trello-cli cards create`  **W**") {
		t.Error("cards create is not marked as changing state")
	}
	if !strings.Contains(out, "### `trello-cli boards list`\n") {
		t.Error("a read command is marked as changing state")
	}
}

// A requirement enforced in the command body is invisible to a tree walk, so
// without the annotation the reference omits its most useful fact.
func TestRequirementsAreRendered(t *testing.T) {
	out := renderFromTree()
	if !strings.Contains(out, "Requires: `--name`") {
		t.Error("a required flag is not documented")
	}
	if !strings.Contains(out, "Requires at least one of:") {
		t.Error("the at-least-one-of requirement is not documented")
	}
}
