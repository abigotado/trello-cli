// Command trello-cli manages Trello from the command line and from AI agents.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/abigotado-niko/trello-cli/internal/cli"
	"github.com/abigotado-niko/trello-cli/internal/errx"
)

func main() {
	os.Exit(int(run()))
}

// run delegates to the command tree.
//
// cli.App.Run already recovers around command execution, where a panic is
// actually likely. This second recover is the outermost net, covering the
// construction of the command tree itself. Both exist for the same reason: a
// compiled Go binary exits 2 when the runtime kills it, and the contract
// assigns 2 to a usage error, so an unrecovered panic would tell an agent it
// called the command wrong.
func run() (code errx.Code) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "internal error: %v\n\n%s\n", r, debug.Stack())
			code = errx.CodeInternal
		}
	}()
	return cli.Execute(os.Args[1:])
}
