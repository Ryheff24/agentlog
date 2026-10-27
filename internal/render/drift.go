package render

import (
	"fmt"
	"io"

	"agentlog/internal/engine"
	"agentlog/internal/transcript"
)

// Drift is the footer a view ends with when the log held records this build
// cannot read. It is the difference between a tool that has gone stale and one
// that has gone stale quietly — and quietly is the one that gets an agent to
// state, with confidence, a conclusion drawn from half a session.
func Drift(w io.Writer, ts ...*transcript.Transcript) {
	if line := engine.Drift(ts...); line != "" {
		fmt.Fprintln(w, line)
	}
}
