package app

import (
	"agentlog/internal/shell"
	"path/filepath"
	"strings"
)

// feedbackCalls returns the message of every `agentlog feedback add` that a
// shell command line runs. Feedback is read back from the agents' own logs,
// where each harness records the command before it runs, so this has to tell
// a real invocation from the same words quoted, echoed, grepped for or written
// into a file: only a word in command position counts, heredoc bodies are
// data, and the script of `bash -c` is read as a command line of its own.
func feedbackCalls(line string) []string {
	return feedbackCallsDepth(line, 0)
}

func feedbackCallsDepth(line string, depth int) []string {
	var out []string
	var prev shell.Command // the command before this one in a pipeline
	for _, c := range shell.Commands(line) {
		args := shell.CommandWords(c.Args)
		switch {
		case len(args) >= 3 && isAgentlog(args[0]) && args[1] == "feedback" && args[2] == "add":
			msg := strings.TrimSpace(strings.Join(args[3:], " "))
			if msg == "-" {
				msg = strings.TrimSpace(c.Stdin)
				if msg == "" && c.Piped {
					msg = strings.TrimSpace(printed(prev))
				}
				// Text agentlog cannot see, such as a file redirected in: the
				// command line is the best record of the note there is.
				if msg == "" {
					msg = strings.TrimSpace(line)
				}
			}
			if msg != "" {
				out = append(out, msg)
			}
		case depth < 3 && len(args) >= 3 && isShell(args[0]):
			for i := 1; i+1 < len(args); i++ {
				if a := args[i]; strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c") {
					out = append(out, feedbackCallsDepth(args[i+1], depth+1)...)
					break
				}
			}
		}
		prev = c
	}
	return out
}

// printed is what a command piped into `agentlog feedback add -` wrote: the
// words of an echo or a printf, or the heredoc a cat passes on.
func printed(c shell.Command) string {
	args := shell.CommandWords(c.Args)
	if len(args) == 0 {
		return ""
	}
	switch args[0] {
	case "echo":
		words := args[1:]
		for len(words) > 0 && len(words[0]) > 1 && words[0][0] == '-' && strings.Trim(words[0][1:], "neE") == "" {
			words = words[1:]
		}
		return strings.Join(words, " ")
	case "printf":
		if len(args) > 2 { // a format, then the text it formats
			return strings.Join(args[2:], " ")
		}
		if len(args) == 2 {
			return strings.NewReplacer(`\n`, "\n", `\t`, "\t").Replace(args[1])
		}
	case "cat":
		if len(args) == 1 {
			return c.Stdin
		}
	}
	return ""
}

func isAgentlog(word string) bool { return filepath.Base(word) == "agentlog" }

func isShell(word string) bool {
	switch filepath.Base(word) {
	case "bash", "sh", "zsh":
		return true
	}
	return false
}
