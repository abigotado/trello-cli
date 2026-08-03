// Package assets carries the files trello-cli ships to other tools.
//
// An embed pattern resolves relative to the directory of the package that
// declares it, which is the only reason this package sits at the repository
// root rather than under internal/. Moving the tree would force
// tools/gencontract and tools/gencommands to move their targets with it, and a
// generator writing one path while the binary embeds another ships a
// permanently stale reference that no CI check can see.
//
// This package imports nothing from this module and must stay that way.
package assets

import "embed"

// FS holds every shipped skill package, rooted at "skills/".
//
// The all: prefix is load bearing. A bare //go:embed skills silently drops
// every file whose name begins with "." or "_" — no warning, no build error,
// just a file that never ships. TestEmbeddedPayloadMatchesTheTree is what
// catches that, because the failure mode is silent omission.
//
//go:embed all:skills
var FS embed.FS
