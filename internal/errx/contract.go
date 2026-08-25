package errx

// docs/contract.md and the shipped skill reference are produced from the table
// below, so a code cannot be renumbered here and left stale elsewhere.
//go:generate go run github.com/abigotado/trello-cli/tools/gencontract

// EnvelopeVersion is the `v` field of every response envelope.
//
// Bump it only when a field is renamed, removed, or changes type. Adding a
// field is additive and does not require a bump.
const EnvelopeVersion = 1

// Code is a trello-cli process exit status.
//
// Each code maps to a distinct caller recovery action. That is the test for
// whether a new one is justified: if the caller's next move is the same as an
// existing code's, it belongs in Error.Reason instead.
type Code int

const (
	// CodeOK signals success.
	CodeOK Code = 0
	// CodeInternal signals a defect in trello-cli. Do not retry.
	CodeInternal Code = 1
	// CodeUsage signals invalid flags or arguments.
	//
	// This is 2 for two reasons. It matches the shell convention, and a
	// compiled Go binary exits 2 when the runtime kills it, so 2 must never
	// carry a meaning that would make a crash look like a legitimate result
	// worth retrying. main installs a recover() that maps panics to
	// CodeInternal, but a runtime fatal error still bypasses it.
	CodeUsage Code = 2
	// CodeNotFound signals that nothing matched. The envelope may carry
	// did_you_mean.
	CodeNotFound Code = 3
	// CodeAmbiguous signals that several objects matched. The envelope carries
	// candidates.
	CodeAmbiguous Code = 4
	// CodeAuth signals that credentials need user action. Error.Hint identifies
	// whether to log in, migrate keychain access, or retry authorization.
	CodeAuth Code = 5
	// CodeRetryable signals a rate limit or transport failure.
	CodeRetryable Code = 6
	// CodeConfirm signals an unconfirmed destructive operation.
	CodeConfirm Code = 7
)

// CodeInfo documents one exit code. It is the source the generated
// docs/contract.md and the shipped skill reference are built from.
type CodeInfo struct {
	Code     Code   `json:"code"`
	Name     string `json:"name"`
	Meaning  string `json:"meaning"`
	NextMove string `json:"next_move"`
}

// codes is the single source of truth for the exit-code contract. Every other
// statement of this table in the repository is generated from it.
var codes = []CodeInfo{
	{CodeOK, "OK", "ok", "proceed"},
	{CodeInternal, "INTERNAL", "internal failure", "report, do not retry"},
	{CodeUsage, "USAGE", "usage or validation error", "fix the flags"},
	{CodeNotFound, "NOT_FOUND", "nothing matched", "check the name; see did_you_mean"},
	{CodeAmbiguous, "AMBIGUOUS", "several objects matched", "pick from candidates"},
	{CodeAuth, "AUTH", "credentials need user action", "follow the hint"},
	{CodeRetryable, "RETRYABLE", "rate limited or network failure", "back off and retry"},
	{CodeConfirm, "CONFIRMATION_REQUIRED", "destructive operation not confirmed", "add --yes"},
}

// Codes returns the exit-code contract.
//
// The slice is copied so a caller cannot mutate the contract in place.
func Codes() []CodeInfo {
	out := make([]CodeInfo, len(codes))
	copy(out, codes)
	return out
}

// Contract is the machine-readable description emitted by `trello-cli contract`.
type Contract struct {
	EnvelopeVersion int        `json:"envelope_version"`
	Codes           []CodeInfo `json:"codes"`
}

// Describe returns the full machine contract.
func Describe() Contract {
	return Contract{EnvelopeVersion: EnvelopeVersion, Codes: Codes()}
}
