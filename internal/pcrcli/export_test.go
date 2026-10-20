package pcrcli

// The CLI tests live in package pcrcli_test so they exercise the command
// surface the way callers do. These aliases are the only internals they
// need: the exit codes they assert on and the two sanitizers they probe
// directly. Keeping the bridge here avoids widening the real API.

const (
	ExitOK     = exitOK
	ExitUsage  = exitUsage
	ExitConfig = exitConfig
)

var (
	TableCell          = tableCell
	SanitizeDiagnostic = sanitizeDiagnostic
)
