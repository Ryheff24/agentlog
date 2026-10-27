package copilot

import (
	"github.com/Ryheff24/agentlog/internal/spec"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// table is what this reader knows about Copilot CLI's events.jsonl. See the
// note in the Claude table: a type is a line here and a case in Load.
func init() {
	spec.Register(spec.Table{
		Provider: transcript.Copilot,
		Records: []spec.Record{
			{"session.auto_mode_resolved", spec.Ignore, "model routing telemetry"},
			{"session.usage_checkpoint", spec.Ignore, "billing and cache checkpoint"},
			{"session.start", spec.Meta, "cwd, model, CLI version"},
			{"session.model_change", spec.Meta, "model switched mid-session"},
			{"user.message", spec.Step, "prompt"},
			{"assistant.message", spec.Step, "model reply"},
			{"assistant.reasoning", spec.Step, "thinking"},
			{"tool.execution_start", spec.Step, "call, paired with its completion"},
			{"tool.execution_complete", spec.Step, "result"},
			{"subagent.started", spec.Step, "names the subagent a task ran"},
			{"subagent.completed", spec.Step, "subagent result"},
			{"subagent.failed", spec.Step, "subagent failure"},
			{"session.error", spec.Step, "error"},
			{"session.warning", spec.Step, "warning, e.g. an MCP server that would not connect"},
			{"abort", spec.Step, "interruption"},
			{"session.compaction_complete", spec.Step, "context was compacted"},
			{"model.turn_failed", spec.Step, "the model call behind a turn failed"},

			{"assistant.turn_start", spec.Ignore, "turn boundary"},
			{"assistant.turn_end", spec.Ignore, "turn boundary"},
			{"turn.completed", spec.Ignore, "turn boundary"},
			{"response.completed", spec.Ignore, "turn boundary"},
			{"hook.start", spec.Ignore, "hook boundary"},
			{"hook.end", spec.Ignore, "hook boundary"},
			{"permission.requested", spec.Ignore, "permission prompt"},
			{"permission.completed", spec.Ignore, "permission prompt"},
			{"session.shutdown", spec.Meta, "per-model tokens and request accounting"},
			{"session.mode_changed", spec.Ignore, "posture"},
			{"session.info", spec.Ignore, "telemetry"},
			{"session.resume", spec.Ignore, "session bookkeeping"},
			{"session.plan_changed", spec.Ignore, "plan, read from the tool call"},
			{"session.task_complete", spec.Ignore, "turn boundary"},
			{"session.compaction_start", spec.Ignore, "paired with the completion"},
			{"system.notification", spec.Ignore, "telemetry"},
			{"system.message", spec.Ignore, "telemetry"},
			{"skill.invoked", spec.Ignore, "shown as the tool call it produces"},
			{"subagent.selected", spec.Ignore, "shown via subagent.started"},
			{"subagent.deselected", spec.Ignore, "pairs with subagent.selected"},
			{"session.permissions_changed", spec.Ignore, "posture"},
			{"model.turn_started", spec.Ignore, "model routing telemetry"},
			{"model.turn_ended", spec.Ignore, "model routing telemetry"},
			{"model.model_call_started", spec.Ignore, "model routing telemetry"},
			{"model.model_call_failure", spec.Ignore, "retried call; a turn that fails says so in model.turn_failed"},
			{"model.captured_assignment_context", spec.Ignore, "experiment assignment telemetry"},
		},
		Tools: map[string]spec.Class{
			"bash": spec.Shell, "shell": spec.Shell,
			"filesystem-edit_file": spec.Edit, "filesystem-write_file": spec.Edit, "str_replace_editor": spec.Edit, "create": spec.Edit, "write": spec.Edit,
			"view": spec.Read, "read": spec.Read, "glob": spec.Read, "grep": spec.Read,
			"task": spec.Agent, "fetch": spec.Web,
		},
	})
}
