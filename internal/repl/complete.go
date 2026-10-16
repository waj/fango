package repl

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/waj/fango/internal/token"
)

// replCommand is one prompt command as `:help` lists it.
type replCommand struct{ name, args, description string }

var replCommands = []replCommand{
	{":type", "<expr>", "show an expression's type without evaluating"},
	{":reload", "", "refresh imported local modules atomically"},
	{":uses", "", "list the handler levels installed with `use`"},
	{":end", "", "end the innermost handler level (also Ctrl-D)"},
	{":help", "", "this message"},
	{":quit", "", "end every level and leave the REPL"},
}

// debugPromptCommands are the debugger commands the ordinary prompt accepts
// in a --debug session.
var debugPromptCommands = []string{":break", ":breaks", ":delete", ":step"}

func writeCommandHelp(w io.Writer) {
	fmt.Fprintln(w, "commands:")
	for _, c := range replCommands {
		fmt.Fprintf(w, "  %-15s%s\n", strings.TrimSpace(c.name+" "+c.args), c.description)
	}
}

// complete is the prompt's Tab completer. It completes the word before the
// cursor: a command at the start of a line, a module path after `import`, and
// otherwise a keyword or a name in prompt scope. A qualified name completes
// one segment at a time, so `Li` offers `List.` rather than every member.
func (s *Session) complete(line string, pos int) (head string, items []string, tail string) {
	start := pos
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(line[:start])
		if !isWordRune(r) {
			break
		}
		start -= size
	}
	head, word, tail := line[:start], line[start:pos], line[pos:]
	if strings.TrimSpace(head) == ":" && !strings.HasSuffix(head, " ") {
		head, word = strings.TrimSuffix(head, ":"), ":"+word
		return head, matches(s.commandNames(), word), tail
	}
	if word == "" {
		return head, nil, tail
	}
	if fields := strings.Fields(head); len(fields) == 1 && fields[0] == "import" && strings.HasSuffix(head, " ") {
		return head, matches(s.graph.Modules(), word), tail
	}
	names := s.prompt.Names()
	for k := range token.Keywords {
		names = append(names, k)
	}
	return head, matches(names, word), tail
}

func isWordRune(r rune) bool {
	return r == '_' || r == '\'' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func (s *Session) commandNames() []string {
	var names []string
	for _, c := range replCommands {
		names = append(names, c.name)
	}
	if s.debug != nil {
		names = append(names, debugPromptCommands...)
	}
	return names
}

// matches keeps the candidates that extend prefix, cut after the first `.`
// past it, sorted and without repeats.
func matches(candidates []string, prefix string) []string {
	var out []string
	for _, c := range candidates {
		rest, ok := strings.CutPrefix(c, prefix)
		if !ok {
			continue
		}
		if i := strings.IndexByte(rest, '.'); i >= 0 {
			c = prefix + rest[:i+1]
		}
		out = append(out, c)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
