package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed diagnostic.fango
var diagnosticWorkload string

//go:embed diagnostic_probe.txt
var diagnosticProbe string

const diagnosticMain = `
reportTyped : Result Json.Error (List Order) ->{IO.IO} ()
reportTyped result =
    case result of
        Err error ->
            print error.message
            Process.exit 1
        Ok orders ->
            total = List.foldl summarize (Totals { records = 0, ids = 0, cents = 0, units = 0, text_bytes = 0 }) orders
            Console.write (show total.records ++ " " ++ show total.ids ++ " " ++ show total.cents ++ " " ++ show total.units ++ " " ++ show total.text_bytes ++ "\n")

runTask : String -> TextReader.Reader e ->{IO.IO | e} ()
runTask kind reader =
    Probe.begin()
    case kind of
        "typed" ->
            result = Json.readText @(List Order) reader
            Probe.finish()
            reportTyped result
        "tokens" ->
            result = Json.Pull.withTextReader reader { scanTokens (ScanTotals { count = 0, strings = 0, numbers = 0, trues = 0, nulls = 0 }) }
            Probe.finish()
            case result of
                Err error ->
                    print error.message
                    Process.exit 1
                Ok total -> Console.write (show total.count ++ " " ++ show total.strings ++ " " ++ show total.numbers ++ " " ++ show total.trues ++ " " ++ show total.nulls ++ "\n")
        "scalars" ->
            (count, codes, bytes) = scanScalars reader 0 0 0
            Probe.finish()
            Console.write (show count ++ " " ++ show codes ++ " " ++ show bytes ++ "\n")
        _ -> Process.exit 1

main() =
    result = Fail.attempt {
        case Process.args() of
            [kind, source, path] ->
                case source of
                    "file" -> File.withFile path { file -> Reader.over (IO.source file) { reader -> TextReader.over reader { text -> runTask kind text } } }
                    _ ->
                        text = Fail.fromResult (File.read path)
                        if kind == "encoding" then
                            bytes = Bytes.fromString text
                            Probe.begin()
                            (count, codes, size) = scanEncoding bytes 0 0 0
                            Probe.finish()
                            Console.write (show count ++ " " ++ show codes ++ " " ++ show size ++ "\n")
                        else case source of
                            "bytes" -> Reader.withBytes (Bytes.fromString text) { reader -> TextReader.over reader { textReader -> runTask kind textReader } }
                            "text" -> TextReader.withString text { reader -> runTask kind reader }
                            _ -> Process.exit 1
            _ -> Process.exit 1
    }
    case result of
        Err error ->
            print (IO.describeError error)
            Process.exit 1
        Ok _ -> ()
`

const emptyDiagnosticCounters = `package fangort
func JSONDiagnosticCounters() map[string]uint64 { return nil }
`

const diagnosticCounters = `package fangort
import "sync/atomic"
var jsonDiagnosticCounts [18]atomic.Uint64
func JSONDiagnosticCount(index int) { jsonDiagnosticCounts[index].Add(1) }
func JSONDiagnosticCounters() map[string]uint64 {
 names := []string{"state_snapshots", "state_commits", "row_extensions", "evidence_lookups", "binding_comparisons", "scalar_decode_probes", "text_inspections", "scalar_reads", "scalar_peeks", "span_reads", "span_scalars", "byte_refills", "byte_skips", "file_reads", "buffer_windows", "buffered_token_attempts", "incremental_tokens", "window_commits"}
 result := map[string]uint64{}
 for i,name := range names { result[name] = jsonDiagnosticCounts[i].Load() }
 return result
}
`

type diagnosticSample struct {
	Variant     string          `json:"variant"`
	Task        string          `json:"task"`
	Source      string          `json:"source"`
	Round       int             `json:"round"`
	WallSeconds float64         `json:"wall_seconds"`
	Stats       json.RawMessage `json:"stats"`
	Verified    bool            `json:"verified"`
}

func diagnosticExpectations(fixture string) (string, string, error) {
	data, err := os.ReadFile(fixture)
	if err != nil {
		return "", "", err
	}
	if !utf8.Valid(data) || !json.Valid(data) {
		return "", "", fmt.Errorf("diagnostics require valid UTF-8 JSON")
	}
	var scalars, codes int64
	for _, char := range string(data) {
		scalars++
		codes += int64(char)
	}
	lexical := regexp.MustCompile(`"(?:\\.|[^"\\])*"|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|null|[{}\[\]:,]`)
	var count, text, numbers, trues, nulls int64
	end := 0
	for _, span := range lexical.FindAllIndex(data, -1) {
		if len(bytes.TrimSpace(data[end:span[0]])) != 0 {
			return "", "", fmt.Errorf("unmatched JSON lexeme at %d", end)
		}
		lexeme := data[span[0]:span[1]]
		count++
		switch lexeme[0] {
		case '"':
			var value string
			if err = json.Unmarshal(lexeme, &value); err != nil {
				return "", "", err
			}
			text += int64(len(value))
		case 't':
			trues++
		case 'n':
			nulls++
		default:
			if lexeme[0] == '-' || lexeme[0] >= '0' && lexeme[0] <= '9' {
				numbers += int64(len(lexeme))
			}
		}
		end = span[1]
	}
	if len(bytes.TrimSpace(data[end:])) != 0 {
		return "", "", fmt.Errorf("unmatched final JSON lexeme")
	}
	return fmt.Sprintf("%d %d %d", scalars, codes, len(data)), fmt.Sprintf("%d %d %d %d %d", count, text, numbers, trues, nulls), nil
}

func runDiagnostics(repo string, env []string, dest, compiler, fixture string, runs int, typedExpected string) error {
	dir := filepath.Join(dest, "diagnostics")
	if err := os.Mkdir(dir, 0755); err != nil {
		return err
	}
	scalarExpected, tokenExpected, err := diagnosticExpectations(fixture)
	if err != nil {
		return err
	}
	products := workload[strings.Index(workload, "type Shipping"):strings.Index(workload, "main() =")]
	source := diagnosticWorkload + "\n" + products + diagnosticMain
	for name, data := range map[string]string{
		"diagnostic.fango": source,
		"Probe.fango":      "module Probe exposing (begin, finish)\n\nimport IO\n\nbegin : () ->{IO.IO} ()\nbegin = native\n\nfinish : () ->{IO.IO} ()\nfinish = native\n",
		"Probe.native.go":  diagnosticProbe,
	} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
			return err
		}
	}
	project := filepath.Join(dir, "baseline")
	if _, err = command(repo, env, compiler, "build", "--emit-go", "-o", project, filepath.Join(dir, "diagnostic.fango")); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(project, "fangort", "json_diagnostic.go"), []byte(emptyDiagnosticCounters), 0644); err != nil {
		return err
	}
	variants := []string{"baseline", "row-pointer", "counters"}
	// Older compiler output still has the two bound cell adapters. Once the
	// compiler has removed them, the diagnostic rewrite has no work to do.
	localModule, err := os.ReadFile(filepath.Join(project, "modules", "Runtime", "Local", "module.go"))
	if err != nil {
		return err
	}
	if strings.Contains(string(localModule), "t_record0.Direct(") || strings.Contains(string(localModule), "t_record1.Direct(") {
		variants = []string{"baseline", "bound-cell", "row-pointer", "combined", "counters"}
	}
	rewrites := map[string]map[string]int{}
	for _, variant := range variants {
		target := filepath.Join(dir, variant)
		if variant != "baseline" {
			if err = copyDiagnosticProject(project, target); err != nil {
				return err
			}
			var changes map[string]int
			changes, err = transformDiagnosticProject(target, variant)
			if err != nil {
				return err
			}
			rewrites[variant] = changes
		}
		if _, err = command(target, env, "go", "build", "-o", filepath.Join(dir, variant+".bin"), "./entries/diagnostic"); err != nil {
			return err
		}
	}
	type job struct{ task, source string }
	jobs := []job{{"encoding", "bytes"}}
	for _, task := range []string{"scalars", "tokens", "typed"} {
		for _, source := range []string{"file", "bytes", "text"} {
			jobs = append(jobs, job{task, source})
		}
	}
	var samples []diagnosticSample
	save := func() error {
		data, e := json.MarshalIndent(map[string]any{"fixture": fixture, "expectations": map[string]string{"typed": typedExpected, "tokens": tokenExpected, "scalars": scalarExpected}, "rewrites": rewrites, "samples": samples}, "", "  ")
		if e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(dir, "results.json"), append(data, '\n'), 0644)
	}
	run := func(variant string, j job, round int) error {
		start := time.Now()
		output, e := command(dir, env, filepath.Join(dir, variant+".bin"), j.task, j.source, fixture)
		wall := time.Since(start).Seconds()
		if e != nil {
			return e
		}
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(lines) != 2 || !json.Valid([]byte(lines[0])) {
			return fmt.Errorf("unexpected diagnostic output: %s", output)
		}
		expected := typedExpected
		if j.task == "tokens" {
			expected = tokenExpected
		} else if j.task == "scalars" || j.task == "encoding" {
			expected = scalarExpected
		}
		s := diagnosticSample{variant, j.task, j.source, round, wall, json.RawMessage(lines[0]), lines[1] == expected}
		samples = append(samples, s)
		if err = save(); err != nil {
			return err
		}
		var stats struct {
			Compute float64 `json:"compute_seconds"`
		}
		_ = json.Unmarshal(s.Stats, &stats)
		fmt.Printf("diagnostic %s %s/%s %d: %.3fs compute, verified=%v\n", variant, j.task, j.source, round, stats.Compute, s.Verified)
		if !s.Verified {
			return fmt.Errorf("diagnostic checksum mismatch: got %s want %s", lines[1], expected)
		}
		return nil
	}
	// Layer comparisons use the same baseline executable; reverse each round.
	for round := 1; round <= runs; round++ {
		for k := range jobs {
			i := k
			if round%2 == 0 {
				i = len(jobs) - 1 - k
			}
			if err = run("baseline", jobs[i], round); err != nil {
				return err
			}
		}
	}
	// Alternate each variant with a fresh baseline, for the whole typed file path.
	for _, variant := range variants[1 : len(variants)-1] {
		for round := 1; round <= runs; round++ {
			pair := []string{"baseline", variant}
			if round%2 == 0 {
				pair[0], pair[1] = pair[1], pair[0]
			}
			for _, name := range pair {
				if err = run(name, job{"typed", "file"}, round); err != nil {
					return err
				}
			}
		}
	}
	// Counter-only runs never contribute to the timing comparisons.
	for _, j := range jobs {
		if err = run("counters", j, 0); err != nil {
			return err
		}
	}
	for _, variant := range variants[1 : len(variants)-1] {
		name := variant + "-counters"
		target := filepath.Join(dir, name)
		if err = copyDiagnosticProject(filepath.Join(dir, variant), target); err != nil {
			return err
		}
		changes, e := transformDiagnosticProject(target, "counters")
		if e != nil {
			return e
		}
		rewrites[name] = changes
		if _, err = command(target, env, "go", "build", "-o", filepath.Join(dir, name+".bin"), "./entries/diagnostic"); err != nil {
			return err
		}
		if err = run(name, job{"typed", "file"}, 0); err != nil {
			return err
		}
		base := samples[len(samples)-2].Stats
		// Compare against the baseline typed/file counters, not the preceding variant.
		for _, sample := range samples {
			if sample.Variant == "counters" && sample.Task == "typed" && sample.Source == "file" {
				base = sample.Stats
				break
			}
		}
		var before, after struct {
			Counters map[string]uint64 `json:"counters"`
		}
		if err = json.Unmarshal(base, &before); err != nil {
			return err
		}
		if err = json.Unmarshal(samples[len(samples)-1].Stats, &after); err != nil {
			return err
		}
		for _, key := range []string{"state_snapshots", "state_commits", "scalar_decode_probes", "scalar_reads", "scalar_peeks", "span_reads", "byte_refills", "byte_skips", "file_reads", "buffer_windows", "buffered_token_attempts", "incremental_tokens", "window_commits"} {
			if before.Counters[key] != after.Counters[key] {
				return fmt.Errorf("%s changed %s: %d versus %d", variant, key, after.Counters[key], before.Counters[key])
			}
		}
	}
	// Exercise the diagnostic row changes against the runtime's existing
	// shadowing, applied-effect, and task-rebasing contracts after timing ends.
	for _, variant := range variants {
		if variant != "row-pointer" && variant != "combined" {
			continue
		}
		target := filepath.Join(dir, variant)
		for _, name := range []string{"evidence_test.go", "evidence_value_test.go", "evidence_fork_test.go"} {
			data, e := os.ReadFile(filepath.Join(repo, "runtime", "fangort", name))
			if e != nil {
				return e
			}
			if err = os.WriteFile(filepath.Join(target, "fangort", name), data, 0644); err != nil {
				return err
			}
		}
		output, e := command(target, env, "go", "test", "./fangort")
		if e != nil {
			return e
		}
		if err = os.WriteFile(filepath.Join(dir, variant+"-verification.txt"), output, 0644); err != nil {
			return err
		}
	}
	return nil
}

func copyDiagnosticProject(source, dest string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, e := filepath.Rel(source, path)
		if e != nil {
			return e
		}
		target := filepath.Join(dest, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		return os.WriteFile(target, data, 0644)
	})
}

func transformDiagnosticProject(project, variant string) (map[string]int, error) {
	counts := map[string]int{}
	if variant == "row-pointer" || variant == "combined" {
		// Pass immutable bindings by pointer instead of copying their entire records.
		for _, name := range []string{"evidence.go", "evidence_value.go"} {
			path := filepath.Join(project, "fangort", name)
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			text := string(data)
			if name == "evidence.go" {
				text = strings.Replace(text, "binding EvidenceBinding) bool", "binding *EvidenceBinding) bool", 1)
			}
			re := regexp.MustCompile(`sameEvidenceBinding\(([^\n]*), \*([A-Za-z][A-Za-z0-9_]*)\)`)
			counts["binding_pointer_arguments"] += len(re.FindAllString(text, -1))
			text = re.ReplaceAllString(text, `sameEvidenceBinding($1, $2)`)
			if name == "evidence_value.go" {
				text = strings.Replace(text, "func ExtendEvidenceValue(row EvidenceValue, bindings ...*EvidenceBinding) EvidenceValue {", "func ExtendEvidenceValue(row EvidenceValue, bindings ...*EvidenceBinding) EvidenceValue {\nif len(bindings)==1 && bindings[0]!=nil && row.inline[0]==bindings[0] { return row }", 1)
				counts["first_binding_fast_path"]++
			}
			if err = os.WriteFile(path, []byte(text), 0644); err != nil {
				return nil, err
			}
		}
	}
	if variant == "row-pointer" || variant == "combined" {
		path := filepath.Join(project, "fangort", "evidence_fork.go")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		updated := strings.ReplaceAll(string(data), "sameEvidenceBinding(binding.Name, binding.Arguments, prior)", "sameEvidenceBinding(binding.Name, binding.Arguments, &prior)")
		if err = os.WriteFile(path, []byte(updated), 0644); err != nil {
			return nil, err
		}
	}
	if variant == "bound-cell" || variant == "combined" {
		path := filepath.Join(project, "modules", "Runtime", "Local", "module.go")
		err := rewriteDiagnosticGo(path, func(file *ast.File) {
			ast.Inspect(file, func(n ast.Node) bool {
				block, ok := n.(*ast.BlockStmt)
				if !ok {
					return true
				}
				for i, statement := range block.List {
					ret, ok := statement.(*ast.ReturnStmt)
					if !ok || len(ret.Results) != 1 {
						continue
					}
					call, ok := ret.Results[0].(*ast.CallExpr)
					if !ok || len(call.Args) != 3 {
						continue
					}
					selector, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					id, ok := selector.X.(*ast.Ident)
					if !ok || id.Name != "t_record0" && id.Name != "t_record1" {
						continue
					}
					if selector.Sel.Name != "Direct" && selector.Sel.Name != "Exit" {
						continue
					}
					// Only the cell adapters whose first argument is their fixed activation.
					evidence := call.Args[0]
					fixed := false
					switch x := evidence.(type) {
					case *ast.Ident:
						fixed = strings.HasPrefix(x.Name, "ev")
					case *ast.SelectorExpr:
						y, ok := x.X.(*ast.Ident)
						fixed = ok && strings.HasPrefix(y.Name, "ev") && x.Sel.Name == "Exit"
					}
					if !fixed {
						continue
					}
					operation := "Op_Runtime_dot_Local_dot_readState"
					var args []ast.Expr
					if id.Name == "t_record1" {
						operation = "Op_Runtime_dot_Local_dot_writeState"
						args = []ast.Expr{call.Args[2]}
					}
					direct := &ast.CallExpr{Fun: &ast.SelectorExpr{X: evidence, Sel: ast.NewIdent(operation)}, Args: args}
					if id.Name == "t_record1" && selector.Sel.Name == "Direct" {
						block.List[i] = &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: direct}, &ast.ReturnStmt{Results: []ast.Expr{&ast.SelectorExpr{X: ast.NewIdent("fangort"), Sel: ast.NewIdent("UnitValue")}}}}}
					} else {
						ret.Results[0] = direct
					}
					counts["bound_cell_calls"]++
				}
				return true
			})
			// Keep discarded adapter construction available to Go's dead-code
			// analysis without leaving unused generated locals.
			ast.Inspect(file, func(n ast.Node) bool {
				block, ok := n.(*ast.BlockStmt)
				if !ok {
					return true
				}
				var statements []ast.Stmt
				for _, statement := range block.List {
					statements = append(statements, statement)
					decl, ok := statement.(*ast.DeclStmt)
					if !ok {
						continue
					}
					gen, ok := decl.Decl.(*ast.GenDecl)
					if !ok {
						continue
					}
					for _, spec := range gen.Specs {
						value, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for _, name := range value.Names {
							if name.Name == "t_record0" || name.Name == "t_record1" {
								statements = append(statements, &ast.AssignStmt{Tok: token.ASSIGN, Lhs: []ast.Expr{ast.NewIdent("_")}, Rhs: []ast.Expr{ast.NewIdent(name.Name)}})
							}
						}
					}
				}
				block.List = statements
				return true
			})
		})
		if err != nil {
			return nil, err
		}
		if counts["bound_cell_calls"] == 0 {
			return nil, fmt.Errorf("could not locate bound cell adapters")
		}
	}
	if variant == "counters" {
		if err := os.WriteFile(filepath.Join(project, "fangort", "json_diagnostic.go"), []byte(diagnosticCounters), 0644); err != nil {
			return nil, err
		}
		err := filepath.WalkDir(project, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			relative, _ := filepath.Rel(project, path)
			return rewriteDiagnosticGo(path, func(file *ast.File) {
				imported := false
				countCall := func(slot int) ast.Stmt {
					var fn ast.Expr = ast.NewIdent("JSONDiagnosticCount")
					if !strings.HasPrefix(relative, "fangort/") {
						fn = &ast.SelectorExpr{X: ast.NewIdent("fangort"), Sel: ast.NewIdent("JSONDiagnosticCount")}
						imported = true
					}
					counts[fmt.Sprintf("counter_%d", slot)]++
					return &ast.ExprStmt{X: &ast.CallExpr{Fun: fn, Args: []ast.Expr{&ast.BasicLit{Kind: token.INT, Value: fmt.Sprint(slot)}}}}
				}
				ast.Inspect(file, func(n ast.Node) bool {
					fn, ok := n.(*ast.FuncDecl)
					if !ok || fn.Body == nil {
						return true
					}
					slot := -1
					name := fn.Name.Name
					switch {
					case relative == "fangort/handler_state.go" && name == "Snapshot":
						slot = 0
					case relative == "fangort/handler_state.go" && name == "Store":
						slot = 1
					case relative == "fangort/evidence_value.go" && name == "ExtendEvidenceValue":
						slot = 2
					case relative == "fangort/evidence_value.go" && name == "ValueEvidence":
						slot = 3
					case relative == "fangort/evidence.go" && name == "sameEvidenceBinding":
						slot = 4
					case strings.Contains(relative, "modules/Encoding/") && name == "V_Encoding_dot_decodeAt":
						slot = 5
					case strings.Contains(relative, "modules/Text/Reader/") && strings.HasPrefix(name, "V_Text_dot_Reader_dot_inspect"):
						slot = 6
					case strings.Contains(relative, "modules/Text/Reader/") && strings.HasPrefix(name, "V_Text_dot_Reader_dot_tryReadScalar"):
						slot = 7
					case strings.Contains(relative, "modules/Text/Reader/") && strings.HasPrefix(name, "V_Text_dot_Reader_dot_tryPeekChar"):
						slot = 8
					case strings.Contains(relative, "modules/Text/Reader/") && strings.HasPrefix(name, "V_Text_dot_Reader_dot_tryReadSpan"):
						slot = 9
					case strings.Contains(relative, "native/File/") && name == "ReadHandleBytes":
						slot = 13
					case strings.Contains(relative, "modules/Text/Reader/") && (name == "V_Text_dot_Reader_dot_window" || name == "V_Text_dot_Reader_dot_window_exit"):
						slot = 14
					case strings.Contains(relative, "modules/Json/") && name == "V_Json_dot_fastToken":
						slot = 15
					case strings.Contains(relative, "modules/Json/") && strings.HasPrefix(name, "V_Json_dot_lexIncremental"):
						slot = 16
					case strings.Contains(relative, "modules/Text/Reader/") && strings.HasPrefix(name, "V_Text_dot_Reader_dot_commitWindow"):
						slot = 17
					}
					if slot >= 0 {
						fn.Body.List = append([]ast.Stmt{countCall(slot)}, fn.Body.List...)
					}
					if strings.Contains(relative, "modules/Text/Reader/") && name == "V_Text_dot_Reader_dot_prefixWidth" {
						ast.Inspect(fn.Body, func(n ast.Node) bool {
							loop, ok := n.(*ast.ForStmt)
							if ok {
								loop.Body.List = append([]ast.Stmt{countCall(10)}, loop.Body.List...)
							}
							return true
						})
					}
					if strings.Contains(relative, "modules/Reader/") && (name == "V_Reader_dot_buffering" || name == "V_Reader_dot_buffering_exit" || name == "V_Reader_dot_withBytes" || name == "V_Reader_dot_withBytes_exit") {
						ast.Inspect(fn.Body, func(n ast.Node) bool {
							literal, ok := n.(*ast.CompositeLit)
							if !ok {
								return true
							}
							typ, ok := literal.Type.(*ast.Ident)
							if !ok {
								return true
							}
							slot := -1
							if typ.Name == "Fn1" {
								slot = 11
							} else if typ.Name == "Fn2" {
								slot = 12
							}
							if slot < 0 {
								return true
							}
							for _, element := range literal.Elts {
								field, ok := element.(*ast.KeyValueExpr)
								if !ok {
									continue
								}
								callback, ok := field.Value.(*ast.FuncLit)
								if ok {
									callback.Body.List = append([]ast.Stmt{countCall(slot)}, callback.Body.List...)
								}
							}
							return true
						})
					}
					return true
				})
				if imported {
					exists := false
					for _, im := range file.Imports {
						if im.Path.Value == `"fangobuild/fangort"` {
							exists = true
						}
					}
					if !exists {
						im := &ast.ImportSpec{Path: &ast.BasicLit{Kind: token.STRING, Value: `"fangobuild/fangort"`}}
						file.Decls = append([]ast.Decl{&ast.GenDecl{Tok: token.IMPORT, Specs: []ast.Spec{im}}}, file.Decls...)
					}
				}
			})
		})
		if err != nil {
			return nil, err
		}
	}
	return counts, nil
}

func rewriteDiagnosticGo(path string, rewrite func(*ast.File)) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, path, data, parser.ParseComments)
	if err != nil {
		return err
	}
	rewrite(file)
	var output bytes.Buffer
	if err = format.Node(&output, fs, file); err != nil {
		return err
	}
	return os.WriteFile(path, output.Bytes(), 0644)
}
