// Package shell reads command positions, quotes, redirects and heredocs.
// It is shared by feedback harvesting and usage invocation counting.
package shell

import "strings"

// Commands returns simple commands with quoted data and heredocs kept apart.
func Commands(line string) []Command { return shellCommands(shellLex(line)) }

// commandWords drops what runs a command without being it: variable
// assignments and wrappers such as `env` or `time`, and `go run <pkg>`.
func CommandWords(args []string) []string {
	for len(args) > 0 {
		a := args[0]
		switch {
		case strings.Contains(a, "=") && !strings.HasPrefix(a, "="):
			args = args[1:]
		case a == "do" || a == "then" || a == "env" || a == "command" || a == "exec" || a == "time" || a == "nohup":
			args = args[1:]
		case a == "go" && len(args) > 2 && args[1] == "run":
			args = args[2:]
		default:
			return args
		}
	}
	return args
}

// shellCommand is one simple command: its words, with redirection targets
// removed, and the body of any heredoc feeding it.
type Command struct {
	Args  []string
	Stdin string
	Piped bool // its input is the previous command's output
}

// shellCommands splits a token stream at control operators.
func shellCommands(toks []shellTok) []Command {
	var out []Command
	cur := Command{}
	flush := func(pipe bool) {
		if len(cur.Args) > 0 || cur.Stdin != "" {
			out = append(out, cur)
		}
		cur = Command{Piped: pipe}
	}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if !t.op {
			cur.Args = append(cur.Args, t.text)
			continue
		}
		switch t.text {
		case "|", "|&":
			flush(true)
		case "||", "&&", ";", ";;", "&", "(", ")", "\n":
			flush(false)
		case "<<", "<<-":
			cur.Stdin = t.body
			i++ // the delimiter
		default: // any other redirection: its target is not an argument
			i++
		}
	}
	flush(false)
	return out
}

// shellTok is a word, with quotes removed, or an unquoted operator. A heredoc
// operator carries the body that followed its line.
type shellTok struct {
	text string
	op   bool
	body string
}

// shellLex tokenises a POSIX-ish command line: quotes, backslashes, comments,
// control and redirection operators, and heredocs. It aims to be right about
// the command lines agents write, not to be a shell.
func shellLex(s string) []shellTok {
	var toks []shellTok
	var word strings.Builder
	inWord := false
	var pending []int // heredoc operator indexes waiting for their body
	end := func() {
		if inWord {
			toks = append(toks, shellTok{text: word.String()})
			word.Reset()
			inWord = false
		}
	}
	op := func(text string) {
		end()
		toks = append(toks, shellTok{text: text, op: true})
	}

	r := []rune(s)
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == ' ' || c == '\t':
			end()
		case c == '\n':
			op("\n")
			// Bodies of heredocs opened on this line follow it.
			for _, at := range pending {
				delim, strip := heredocDelim(toks, at)
				var body []string
				for i+1 < len(r) {
					j := i + 1
					k := j
					for k < len(r) && r[k] != '\n' {
						k++
					}
					text := string(r[j:k])
					i = k
					if strip { // <<- drops leading tabs from the body and the delimiter
						text = strings.TrimLeft(text, "\t")
					}
					if text == delim {
						break
					}
					body = append(body, text)
				}
				toks[at].body = strings.Join(body, "\n")
			}
			pending = nil
		case c == '#' && !inWord:
			for i+1 < len(r) && r[i+1] != '\n' {
				i++
			}
		case c == '\\':
			if i+1 < len(r) {
				i++
				if r[i] != '\n' {
					word.WriteRune(r[i])
					inWord = true
				}
			}
		case c == '\'':
			inWord = true
			for i+1 < len(r) && r[i+1] != '\'' {
				i++
				word.WriteRune(r[i])
			}
			i++
		case c == '$' && i+1 < len(r) && r[i+1] == '\'':
			inWord = true
			i++
			for i+1 < len(r) && r[i+1] != '\'' {
				i++
				if r[i] == '\\' && i+1 < len(r) {
					i++
					switch r[i] {
					case 'n':
						word.WriteRune('\n')
					case 't':
						word.WriteRune('\t')
					default:
						word.WriteRune(r[i])
					}
					continue
				}
				word.WriteRune(r[i])
			}
			i++
		case c == '"':
			inWord = true
			for i+1 < len(r) && r[i+1] != '"' {
				i++
				if r[i] == '\\' && i+1 < len(r) && strings.ContainsRune("\"\\$`\n", r[i+1]) {
					i++
					if r[i] == '\n' {
						continue
					}
				}
				word.WriteRune(r[i])
			}
			i++
		case c == '|' || c == '&' || c == ';' || c == '(' || c == ')':
			two := ""
			if i+1 < len(r) {
				two = string(r[i : i+2])
			}
			switch two {
			case "||", "&&", ";;", "|&", "&>":
				op(two)
				i++
			default:
				op(string(c))
			}
		case c == '<' || c == '>':
			// A word of digits right before a redirection is its file descriptor.
			if inWord && isDigits(word.String()) {
				word.Reset()
				inWord = false
			}
			end()
			j := i + 1
			for j < len(r) && j-i < 3 && strings.ContainsRune("<>&|-", r[j]) {
				j++
			}
			text := string(r[i:j])
			if text == "<<-" || text == "<<" {
				toks = append(toks, shellTok{text: text, op: true})
				pending = append(pending, len(toks)-1)
			} else {
				toks = append(toks, shellTok{text: text, op: true})
			}
			i = j - 1
		default:
			word.WriteRune(c)
			inWord = true
		}
	}
	end()
	return toks
}

// heredocDelim is the delimiter word after the heredoc operator at index at.
func heredocDelim(toks []shellTok, at int) (delim string, strip bool) {
	if at+1 < len(toks) && !toks[at+1].op {
		delim = toks[at+1].text
	}
	return delim, toks[at].text == "<<-"
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
