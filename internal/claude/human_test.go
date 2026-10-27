package claude

import (
	"testing"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

func TestQueuedHumanAndSyntheticPrompts(t *testing.T) {
	notify := "<task-notification><status>done</status><summary>build</summary></task-notification>"
	peer := "<cross-session-message from-name=\"peer\">merge it</cross-session-message>"
	queue := func(text string) map[string]any {
		return map[string]any{"type": "queue-operation", "operation": "enqueue", "content": text}
	}
	attach := func(text string) map[string]any {
		return map[string]any{"type": "attachment", "attachment": map[string]any{"type": "queued_command", "prompt": text}}
	}
	tr := loadLog(t, transcript.Load{}, queue(notify), attach(notify), queue(peer), attach(peer), queue("human queued"), attach("human queued"), queue("unanswered human"), user("<system-reminder>context</system-reminder>\nactual human"))
	humans, queued, unanswered := 0, 0, 0
	for _, e := range tr.Events {
		if e.Human {
			humans++
			if e.Tag == transcript.TagQueued {
				queued++
			}
			if e.Tag == transcript.TagUnanswered {
				unanswered++
			}
		}
		if e.Kind == transcript.KindUser && !e.Human {
			t.Errorf("synthetic user leaked: %+v", e)
		}
	}
	if humans != 3 || queued != 1 || unanswered != 1 {
		t.Fatalf("human %d queued %d unanswered %d: %+v", humans, queued, unanswered, tr.Events)
	}
}

func TestRefusalDetailsBecomeError(t *testing.T) {
	for _, reason := range []bool{true, false} {
		r := assistant(map[string]any{"type": "text", "text": "I cannot do that."})
		m := r["message"].(map[string]any)
		if reason {
			m["stop_reason"] = "refusal"
		} else {
			m["stop_details"] = map[string]any{"type": "refusal", "category": "safety", "explanation": "blocked"}
		}
		tr := loadLog(t, transcript.Load{}, r)
		if len(tr.Events) != 1 || tr.Events[0].Tag != "refusal" || tr.Session.Ending != transcript.EndingError {
			t.Fatalf("%+v", tr)
		}
	}
}
