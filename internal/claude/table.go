package claude

import (
	"github.com/Ryheff24/agentlog/internal/spec"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// table is everything this reader knows about Claude Code's log format,
// written down where it can be read, reported on by `agentlog doctor`, and
// checked against the reader by TestTableMatchesTheReader.
//
// Adding a type is a line here and a case in entry(); the test fails until
// both exist, which is the point of writing it down twice.
func init() {
	spec.Register(spec.Table{
		Provider: transcript.Claude,
		Records: []spec.Record{
			{"pr-link", spec.Step, "recorded pull request linkage"},
			{"assistant", spec.Step, "model turn: text, thinking, tool_use"},
			{"user", spec.Step, "prompt, or a tool_result coming back"},
			{"system", spec.Step, "hooks, slash commands, notices"},
			{"attachment", spec.Step, "queued prompts and pasted content"},
			{"queue-operation", spec.Step, "prompts typed while the model was busy"},
			{"cost-state", spec.Meta, "the harness's own account of the thread"},
			{"custom-title", spec.Meta, "title the person set"},
			{"ai-title", spec.Meta, "title the harness generated"},
			{"continued-in", spec.Meta, "the session this conversation went on in"},
			{"relocated", spec.Meta, "the working directory moved (into a worktree)"},
			{"worktree-state", spec.Meta, "the worktree the session entered, or null on leaving"},
			{"agent-setting", spec.Meta, "the agent definition the session runs as"},

			{"last-prompt", spec.Ignore, "duplicate of the last user record"},
			{"mode", spec.Ignore, "posture flag"},
			{"permission-mode", spec.Ignore, "posture flag"},
			{"atis-latch", spec.Ignore, "internal latch"},
			{"bridge-session", spec.Ignore, "session bookkeeping"},
			{"agent-name", spec.Ignore, "subagent naming, read from the child log"},
			{"file-history-snapshot", spec.Ignore, "the harness's own file store"},
			{"file-history-delta", spec.Ignore, "the harness's own file store"},
			{"frame-link", spec.Ignore, "internal linkage"},
			{"api-request", spec.Ignore, "a copy of one request sent to the model, by reference"},
			{"api-request-shape", spec.Ignore, "the system prompt and tool definitions those requests used"},
			{"api-request-blob", spec.Ignore, "one piece of a request's content, stored once"},
			{"tools-used", spec.Ignore, "the tools the session used, already read from its tool calls"},
			{"artifact-comment-monitor", spec.Ignore, "the artifacts the session watches for comments"},
			{"artifact-autoreact-ledger", spec.Ignore, "the harness's record of artifact comment threads it reacted to"},

			{"summary", spec.Ignore, "compaction summary, shown via the compaction notice"},
		},
		Tools: map[string]spec.Class{
			"Bash": spec.Shell, "BashOutput": spec.Shell, "KillShell": spec.Shell,
			"Edit": spec.Edit, "Write": spec.Edit, "NotebookEdit": spec.Edit,
			"Read": spec.Read, "Glob": spec.Read, "Grep": spec.Read,
			"Task": spec.Agent, "Agent": spec.Agent, "SendMessage": spec.Agent,
			"WebFetch": spec.Web, "WebSearch": spec.Web,
		},
	})
}
