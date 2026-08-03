package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/abigotado/trello-cli/internal/errx"
)

// targets are the generated copies of the contract, relative to the repository
// root. The shipped skill reference is generated too, which is what makes the
// claim that shipped skills cannot drift from the binary actually true.
var targets = []string{
	filepath.Join("docs", "contract.md"),
	filepath.Join("assets", "skills", "trello", "reference", "contract.md"),
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gencontract: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	content := renderMarkdown(errx.Describe())
	for _, target := range targets {
		path := filepath.Join(root, target)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		fmt.Println("wrote", target)
	}
	return nil
}

// repoRoot walks up from the working directory to the directory holding go.mod,
// so the generator works from any package via `go generate ./...`.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above the working directory")
		}
		dir = parent
	}
}
