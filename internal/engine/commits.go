package engine

import "agentlog/internal/transcript"

// Commit is a repository operation and the event that performed it, so a
// reader can go from the sha back to the work that produced it.
type Commit struct {
	transcript.GitOp
	Event int `json:"event"`
}

// Commits is what a session landed, in order. It is the answer to the question
// a finished session is most often asked — "did this ship, and as what" — and
// the sha appears nowhere else in the log: the command output says "1 file
// changed", not what it is called.
func Commits(ts ...*transcript.Transcript) []Commit {
	var out []Commit
	for _, t := range ts {
		for _, e := range t.Events {
			for _, g := range e.Git {
				if g.SHA != "" {
					out = append(out, Commit{GitOp: g, Event: e.Index})
				}
			}
		}
	}
	return out
}

// Pushes is every push a session recorded, in order: whether the commits
// left the machine is the next thing asked after what they were.
func Pushes(ts ...*transcript.Transcript) []Commit {
	var out []Commit
	for _, t := range ts {
		for _, e := range t.Events {
			for _, g := range e.Git {
				if g.Action == "pushed" {
					out = append(out, Commit{GitOp: g, Event: e.Index})
				}
			}
		}
	}
	return out
}
