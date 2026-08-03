// Package arch holds no code. It exists so the architecture invariants in
// .agents/rules/architecture.md are enforced by a test rather than by review.
//
// The invariants are cheap to state and easy to break with a single
// convenience import, and every one of them is invisible until something else
// fails much later.
package arch
