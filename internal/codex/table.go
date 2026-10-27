package codex

import (
	"agentlog/internal/spec"
	"agentlog/internal/transcript"
)

// table is what this reader knows about Codex rollouts. Codex writes the same
// turn twice, in two formats, so the table has three families: the top-level
// record types, the legacy response_item/event_msg payloads, and the typed
// items. See the note in the Claude table.
func init() {
	spec.Register(spec.Table{
		Provider: transcript.Codex,
		Records: []spec.Record{
			{"turn_context", spec.Meta, "model and cwd for the turn"},
			{"compacted", spec.Step, "context was compacted"},
			{"session_meta", spec.Meta, "id, cwd, git, CLI version, fork parent"},
			{"token_usage_record", spec.Ignore, "per-turn usage, totalled from token_count"},
			{"world_state", spec.Ignore, "harness state snapshot"},
			{"inter_agent_communication", spec.Ignore, "shown as agent_message"},
			{"inter_agent_communication_metadata", spec.Ignore, "routing"},

			{"event_msg:item_completed", spec.Step, "the typed item stream"},
			{"event_msg:user_message", spec.Step, "prompt"},
			{"event_msg:exec_command_end", spec.Step, "command result"},
			{"event_msg:patch_apply_end", spec.Step, "edit result"},
			{"event_msg:turn_aborted", spec.Step, "interruption"},
			{"event_msg:error", spec.Step, "error"},
			{"event_msg:task_complete", spec.Step, "turn ended"},
			{"event_msg:thread_goal_updated", spec.Meta, "goal"},
			{"event_msg:thread_name_updated", spec.Meta, "title"},
			{"event_msg:thread_rolled_back", spec.Step, "rewind"},
			{"event_msg:entered_review_mode", spec.Step, "review mode"},
			{"event_msg:exited_review_mode", spec.Step, "review mode"},
			{"event_msg:collab_agent_spawn_end", spec.Step, "subagent spawned"},
			{"event_msg:token_count", spec.Meta, "running token total and rate limits"},
			{"event_msg:task_started", spec.Ignore, "turn boundary"},
			{"event_msg:thread_settings_applied", spec.Ignore, "posture"},
			{"event_msg:agent_message", spec.Ignore, "repeats the assistant response_item after it"},

			{"response_item:message", spec.Step, "prompt or reply, by message.role"},
			{"response_item:reasoning", spec.Step, "thinking"},
			{"response_item:function_call", spec.Step, "tool call"},
			{"response_item:function_call_output", spec.Step, "tool result"},
			{"response_item:custom_tool_call", spec.Step, "tool call, by name: apply_patch, exec"},
			{"response_item:custom_tool_call_output", spec.Step, "tool result"},
			{"response_item:web_search_call", spec.Step, "web search"},
			{"response_item:agent_message", spec.Step, "message between agents"},
			{"response_item:tool_search_call", spec.Ignore, "dynamic tool discovery"},
			{"response_item:tool_search_output", spec.Ignore, "dynamic tool discovery"},
			{"response_item:compaction", spec.Ignore, "encrypted, shown via compacted"},

			{"item:UserMessage", spec.Step, "prompt"},
			{"item:AgentMessage", spec.Step, "reply"},
			{"item:Reasoning", spec.Step, "thinking"},
			{"item:CommandExecution", spec.Step, "command with its output"},
			{"item:FileChange", spec.Step, "edit with its diff"},
			{"item:McpToolCall", spec.Step, "MCP call"},
			{"item:DynamicToolCall", spec.Step, "discovered tool call"},
			{"item:WebSearch", spec.Step, "web search"},
			{"item:Extension", spec.Step, "extension call"},
			{"item:ImageView", spec.Step, "image opened"},
			{"item:SubAgentActivity", spec.Step, "subagent start and end, with its thread id"},
			{"item:Plan", spec.Step, "plan"},
			{"item:ContextCompaction", spec.Ignore, "shown via compacted"},
			{"item:CollabAgentToolCall", spec.Ignore, "shown as the function call"},
			{"item:EnteredReviewMode", spec.Ignore, "shown via event_msg"},
			{"item:ExitedReviewMode", spec.Ignore, "shown via event_msg"},
		},
		Tools: map[string]spec.Class{
			"shell": spec.Shell, "exec_command": spec.Shell, "shell_command": spec.Shell,
			"container.exec": spec.Shell, "local_shell": spec.Shell, "write_stdin": spec.Shell,
			"apply_patch": spec.Edit,
			"read_file":   spec.Read, "view_image": spec.Read,
			"subagent": spec.Agent, "web_search": spec.Web,
		},
	})
}
