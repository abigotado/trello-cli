package arch_test

import (
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/abigotado/trello-cli"

// deps returns every package pkg depends on, transitively.
func deps(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	return strings.Fields(string(out))
}

// imports returns only the direct, non-test imports of pkg.
func imports(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, pkg).Output()
	if err != nil {
		t.Fatalf("go list %s: %v", pkg, err)
	}
	return strings.Fields(string(out))
}

// Credentials must arrive as a value in the client constructor. Importing auth
// here would invert the dependency arrow and make every client test depend on
// an OS keychain being present and unlocked.
func TestTrelloDoesNotImportAuth(t *testing.T) {
	for _, dep := range deps(t, module+"/internal/trello") {
		if dep == module+"/internal/auth" {
			t.Error("internal/trello imports internal/auth; pass credentials to trello.New instead")
		}
	}
}

// errx sits at the bottom of the stack. A single import from this module here
// turns the dependency graph into a cycle, which is why errx.Candidate exists
// instead of reusing a Trello model type.
func TestErrxImportsNothingFromThisModule(t *testing.T) {
	for _, dep := range deps(t, module+"/internal/errx") {
		if dep != module+"/internal/errx" && strings.HasPrefix(dep, module) {
			t.Errorf("internal/errx imports %s; it must stay dependency-free", dep)
		}
	}
}

// A command that needs a request the client does not expose gets the method
// added to internal/trello. Reaching for net/http in a command puts transport
// concerns — pacing, retry, redaction — outside the one place that handles them.
func TestCLIDoesNotUseNetHTTPDirectly(t *testing.T) {
	for _, imp := range imports(t, module+"/internal/cli") {
		if imp == "net/http" {
			t.Error("internal/cli imports net/http; add the method to internal/trello instead")
		}
	}
}

// Rendering belongs to internal/output. If the client could import it, model
// types would start carrying display logic and the client would stop being
// testable without a writer.
func TestTrelloDoesNotImportOutput(t *testing.T) {
	for _, dep := range deps(t, module+"/internal/trello") {
		if dep == module+"/internal/output" {
			t.Error("internal/trello imports internal/output; it must not format user-facing text")
		}
	}
}

// config holds non-secret defaults only. If it could read credentials, a
// config file that holds a token becomes possible, and that is a config file
// someone eventually commits.
func TestConfigDoesNotImportAuth(t *testing.T) {
	for _, dep := range deps(t, module+"/internal/config") {
		if dep == module+"/internal/auth" {
			t.Error("internal/config imports internal/auth; configuration must not touch credentials")
		}
	}
}

// resolve turns names into ids; rendering belongs to internal/output. If it
// could import output, resolution results would start carrying display logic
// and the resolver would stop being testable without a writer.
func TestResolveDoesNotImportOutput(t *testing.T) {
	for _, dep := range deps(t, module+"/internal/resolve") {
		if dep == module+"/internal/output" {
			t.Error("internal/resolve imports internal/output; it must not render user-facing text")
		}
	}
}

// resolve sits below cli. The compiler already rejects the cycle, but stating
// it keeps the intended direction explicit alongside the other invariants.
func TestResolveDoesNotImportCLI(t *testing.T) {
	for _, dep := range deps(t, module+"/internal/resolve") {
		if dep == module+"/internal/cli" {
			t.Error("internal/resolve imports internal/cli")
		}
	}
}
