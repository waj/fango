package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/waj/fango/internal/apidoc"
	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/modules"
)

func runDoc(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(append([]string{"doc"}, args...), &out, &errOut)
	return out.String(), errOut.String(), code
}

func decodeDoc(t *testing.T, stdout string) apidoc.Document {
	t.Helper()
	var doc apidoc.Document
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not the JSON document: %v\n%s", err, stdout)
	}
	return doc
}

func declaration(t *testing.T, doc apidoc.Document, id string) apidoc.Declaration {
	t.Helper()
	for _, m := range doc.Modules {
		for _, d := range m.Declarations {
			if d.ID == id {
				return d
			}
		}
	}
	t.Fatalf("no declaration %s", id)
	return apidoc.Declaration{}
}

func TestDocArguments(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		args   []string
		stderr string
	}{
		{"no stdlib", nil, "--stdlib is required"},
		{"unknown module", []string{"--stdlib", "--module", "Maybe", "--module", "Nope"}, `unknown standard-library module "Nope"`},
		{"module case", []string{"--stdlib", "--module", "maybe"}, `unknown standard-library module "maybe"`},
		{"positional", []string{"--stdlib", "Maybe"}, "usage:"},
		{"unknown flag", []string{"--stdlib", "--html"}, "flag provided but not defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runDoc(t, tc.args...)
			if code != 2 || stdout != "" || !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("code %d, stdout %q, stderr %q; want 2, empty stdout, %q", code, stdout, stderr, tc.stderr)
			}
		})
	}
}

func TestDocModuleFilter(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := runDoc(t, "--stdlib", "--module", "Result", "--module", "Maybe")
	if code != 0 || stderr != "" {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	doc := decodeDoc(t, stdout)
	if doc.SchemaVersion != 1 || len(doc.Modules) != 2 || doc.Modules[0].Name != "Maybe" || doc.Modules[1].Name != "Result" {
		t.Fatalf("modules = %+v", doc.Modules)
	}
	maybe := doc.Modules[0]
	if maybe.Source != (apidoc.Location{Path: "stdlib/Maybe.fango", Line: 11}) || !strings.HasPrefix(maybe.Documentation, "Optional values.") {
		t.Fatalf("Maybe module = %+v", maybe)
	}
	var ids []string
	for _, d := range maybe.Declarations {
		ids = append(ids, d.ID)
	}
	if want := []string{"constructor:Maybe.Just", "constructor:Maybe.Nothing", "type:Maybe.Maybe", "value:Maybe.withDefault"}; !slices.Equal(ids, want) {
		t.Fatalf("Maybe declarations = %v, want %v", ids, want)
	}
	typ := declaration(t, doc, "type:Maybe.Maybe")
	if typ.Signature != "type Maybe a = Nothing | Just a" || !slices.Contains(typ.Instances, "Ord a => Ord (Maybe a)") {
		t.Fatalf("Maybe type = %+v", typ)
	}
	just := declaration(t, doc, "constructor:Maybe.Just")
	if just.Signature != "Just : a -> Maybe a" || just.ParentID != "type:Maybe.Maybe" || just.Documentation != "A present value." {
		t.Fatalf("Just = %+v", just)
	}
	// Unannotated functions report their inferred schemes, effect rows included.
	if got := declaration(t, doc, "value:Result.map").Signature; got != "map : (a ->{e} b) -> Result c a ->{e} Result c b" {
		t.Fatalf("Result.map = %q", got)
	}
	if got := declaration(t, doc, "constructor:Result.Ok").Signature; got != "Ok : value -> Result error value" {
		t.Fatalf("Result.Ok = %q", got)
	}
}

// Every bundled module is listed, including those the prelude never reaches,
// and two runs agree byte for byte without naming the machine.
func TestDocWholeLibrary(t *testing.T) {
	t.Parallel()
	first, stderr, code := runDoc(t, "--stdlib")
	if code != 0 || stderr != "" {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	second, _, _ := runDoc(t, "--stdlib")
	if first != second {
		t.Fatal("output differs between runs")
	}
	root, err := libroot.Root()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	for _, local := range []string{root, filepath.ToSlash(root), home, "<stdlib>"} {
		if local != "" && strings.Contains(first, local) {
			t.Fatalf("output names %q", local)
		}
	}
	doc := decodeDoc(t, first)
	want, err := modules.BundledModules()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range doc.Modules {
		names = append(names, m.Name)
		if m.Source.Path != "stdlib/"+strings.ReplaceAll(m.Name, ".", "/")+".fango" || m.Source.Line < 1 {
			t.Errorf("%s source = %+v", m.Name, m.Source)
		}
		if !slices.IsSortedFunc(m.Declarations, func(a, b apidoc.Declaration) int { return strings.Compare(a.ID, b.ID) }) {
			t.Errorf("%s declarations are not sorted by ID", m.Name)
		}
		for i, d := range m.Declarations {
			if i > 0 && m.Declarations[i-1].ID == d.ID {
				t.Errorf("duplicate ID %s", d.ID)
			}
			if d.Signature == "" || d.Source.Line < 1 || !strings.HasPrefix(d.ID, d.Kind+":"+m.Name+".") {
				t.Errorf("incomplete declaration %+v", d)
			}
		}
	}
	if !slices.Equal(names, want) || !slices.Contains(names, "Http.Server.Route") || !slices.Contains(names, "Runtime.Native") {
		t.Fatalf("modules = %v, want %v", names, want)
	}
	for id, sig := range map[string]string{
		// Operators keep their fixity, methods and operations their parent.
		"value:Basics.|>":           "(|>) : a -> (a ->{e} b) ->{e} b",
		"method:Basics.Eq.==":       "(==) : Eq a => a -> a -> Bool",
		"operation:Fail.Fail.fail":  "abort fail : error ->{Fail error} a",
		"operation:State.State.get": "get : () ->{State s} s",
		// Scoped runners keep the pragma that binds their callback row.
		"value:Reader.withBytes": "{-# scoped s #-}\nwithBytes : Bytes -> (Reader s ->{s} a) -> a",
		// Opaque types show no representation.
		"type:Dict.Dict":         "type Dict k v",
		"type:Http.Header":       "type Header = { name : String, value : String }",
		"field:Http.Header.name": "name : String",
	} {
		if got := declaration(t, doc, id).Signature; got != sig {
			t.Errorf("%s signature = %q, want %q", id, got, sig)
		}
	}
	if d := declaration(t, doc, "value:Basics.|>"); d.Fixity != "infixl 0" {
		t.Errorf("(|>) fixity = %q", d.Fixity)
	}
	if d := declaration(t, doc, "method:Basics.Eq.=="); d.ParentID != "class:Basics.Eq" || d.Fixity != "infix 4" {
		t.Errorf("(==) = %+v", d)
	}
	if d := declaration(t, doc, "class:Basics.Eq"); !slices.Contains(d.Instances, "Eq Http.Error") || !slices.Contains(d.Instances, "Eq IO.Error") {
		t.Errorf("Eq instances = %v", d.Instances)
	}
	// Re-exports point at their owner and reuse its signature.
	if d := declaration(t, doc, "type:Json.Value"); d.TargetID != "type:Json.Pull.Value" || !strings.HasPrefix(d.Signature, "type Value = Null |") {
		t.Errorf("Json.Value = %+v", d)
	}
	if d := declaration(t, doc, "constructor:Json.Null"); d.TargetID != "constructor:Json.Pull.Null" || d.ParentID != "type:Json.Value" {
		t.Errorf("Json.Null = %+v", d)
	}
	for _, m := range doc.Modules {
		for _, d := range m.Declarations {
			if m.Name == "Dict" && d.Kind == "constructor" {
				t.Errorf("opaque Dict leaks %s", d.ID)
			}
		}
	}
}

// documentedModules have been migrated to source documentation. Each stays
// complete under --strict, and its examples keep holding.
var documentedModules = []string{
	"Async", "Basics", "Bytes", "Char", "Console", "Derive", "Dict", "Encoding", "Fail", "Failure", "File", "Http", "Http.Client", "Http.GZip", "Http.Server", "Http.Server.Route", "Http.Wire", "IO", "Iterator", "List", "Maybe", "Net",
	"Prelude", "Process", "Random", "Range", "Reader", "Regex", "Result", "Runtime.Local", "Runtime.Native", "Runtime.Prompt",
	"Runtime.Scope", "State", "Stream", "String", "Text.Builder", "Text.Reader", "Text.Writer", "Tuple", "Url", "Writer",
}

func moduleArgs(names []string) []string {
	args := []string{"--stdlib"}
	for _, name := range names {
		args = append(args, "--module", name)
	}
	return args
}

func TestDocStrict(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := runDoc(t, append(moduleArgs(documentedModules), "--strict")...)
	if code != 0 || stderr != "" {
		t.Fatalf("documented modules: code %d, stderr:\n%s", code, stderr)
	}
	decodeDoc(t, stdout)

	// Meta is not migrated yet.
	stdout, stderr, code = runDoc(t, "--stdlib", "--strict", "--module", "Meta")
	if code != 1 || stdout != "" {
		t.Fatalf("code %d, stdout %q; want 1 and no JSON", code, stdout)
	}
	for _, want := range []string{
		"stdlib/Meta.fango:2: module:Meta has no documentation",
		"stdlib/Meta.fango:29: type:Meta.Code has no documentation",
		"missing documentation comment(s)",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	// Without --strict the same module is emitted with empty documentation.
	stdout, stderr, code = runDoc(t, "--stdlib", "--module", "Meta")
	if code != 0 || stderr != "" || decodeDoc(t, stdout).Modules[0].Documentation != "" {
		t.Fatalf("lenient Meta: code %d, stderr %q", code, stderr)
	}
}

// The examples in documented modules are programs: each fenced block runs as
// its own function under both backends, and every assertion line holds.
func TestDocExamplesHold(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := runDoc(t, moduleArgs(documentedModules)...)
	if code != 0 {
		t.Fatalf("doc: %s", stderr)
	}
	doc := decodeDoc(t, stdout)
	var texts []string
	for _, m := range doc.Modules {
		texts = append(texts, m.Documentation)
		for _, d := range m.Declarations {
			if d.TargetID == "" {
				texts = append(texts, d.Documentation)
			}
		}
	}
	program, expected, assertions := exampleProgram(texts)
	if assertions < 10 {
		t.Fatalf("only %d example assertions:\n%s", assertions, program)
	}
	path := filepath.Join(t.TempDir(), "examples.fango")
	if err := os.WriteFile(path, []byte(program), 0o644); err != nil {
		t.Fatal(err)
	}
	runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{}, expected)
}

// exampleProgram turns fenced `fango` blocks into one program. A block's
// imports are its own, as a reader would copy them; they are hoisted to the
// program. Its other top-level items are local declarations when their first
// line binds a name with `=` or `:`, and Bool assertions otherwise;
// continuation lines are indented.
func exampleProgram(texts []string) (program, expected string, assertions int) {
	imports := map[string]bool{}
	var functions, calls, results []string
	for _, text := range texts {
		for _, block := range fencedBlocks(text) {
			var items [][]string
			for _, line := range strings.Split(block, "\n") {
				switch {
				case strings.HasPrefix(line, "import "):
					imports[line] = true
				case strings.TrimSpace(line) == "":
				case strings.HasPrefix(line, " ") && len(items) > 0:
					items[len(items)-1] = append(items[len(items)-1], line)
				default:
					items = append(items, []string{line})
				}
			}
			name := fmt.Sprintf("example%d", len(functions)+1)
			var body, checks []string
			for _, item := range items {
				if declares(item[0]) {
					for _, line := range item {
						body = append(body, "    "+line)
					}
					continue
				}
				checks = append(checks, strings.Join(item, "\n        "))
			}
			if len(checks) == 0 {
				continue
			}
			assertions += len(checks)
			body = append(body, "    [ "+strings.Join(checks, "\n    , ")+"\n    ]")
			functions = append(functions, name+" : () -> List Bool\n"+name+" _ =\n"+strings.Join(body, "\n")+"\n")
			calls = append(calls, name+" ()")
			results = append(results, "["+strings.TrimSuffix(strings.Repeat("True, ", len(checks)), ", ")+"]")
		}
	}
	var b strings.Builder
	b.WriteString(mergeImports(imports))
	b.WriteString("\n" + strings.Join(functions, "\n") + "\nmain = [" + strings.Join(calls, ", ") + "]\n")
	return b.String(), "[" + strings.Join(results, ", ") + "]\n", assertions
}

// mergeImports combines the blocks' imports into one per module, exposing
// everything any block exposed from it.
func mergeImports(lines map[string]bool) string {
	exposed := map[string][]string{}
	for line := range lines {
		module, items, _ := strings.Cut(strings.TrimPrefix(line, "import "), " exposing ")
		module = strings.TrimSpace(module)
		exposed[module] = append(exposed[module], nil...)
		items = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(items), "("), ")")
		for _, item := range strings.Split(items, ",") {
			if item = strings.TrimSpace(item); item != "" && !slices.Contains(exposed[module], item) {
				exposed[module] = append(exposed[module], item)
			}
		}
	}
	var modules []string
	for module := range exposed {
		modules = append(modules, module)
	}
	slices.Sort(modules)
	var b strings.Builder
	for _, module := range modules {
		b.WriteString("import " + module)
		if items := exposed[module]; len(items) > 0 {
			slices.Sort(items)
			b.WriteString(" exposing (" + strings.Join(items, ", ") + ")")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func fencedBlocks(text string) []string {
	var blocks []string
	var current []string
	inside := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case !inside && strings.TrimSpace(line) == "```fango":
			inside, current = true, nil
		case inside && strings.TrimSpace(line) == "```":
			inside = false
			blocks = append(blocks, strings.Join(current, "\n"))
		case inside:
			current = append(current, line)
		}
	}
	return blocks
}

// declares reports whether a top-level example line starts a binding: it
// begins with a plain lowercase name, and a standalone `=` or `:` comes
// before any bracket, quote, or comparison.
func declares(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 || strings.ContainsAny(fields[0], ".([{\"") || fields[0][0] < 'a' || fields[0][0] > 'z' {
		return false
	}
	for _, field := range fields[1:] {
		switch {
		case field == "=" || field == ":":
			return true
		case strings.ContainsAny(field[:1], "([{\"'") || slices.Contains([]string{"==", "/=", "<", ">", "<=", ">=", "&&", "||"}, field):
			return false
		}
	}
	return false
}
