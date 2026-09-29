// Package lsp serves source navigation and diagnostics over the Language
// Server Protocol. The compiler remains the source of resolution and types.
package lsp

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
	"go.lsp.dev/jsonrpc2"
)

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
type rng struct {
	Start position `json:"start"`
	End   position `json:"end"`
}
type location struct {
	URI   string `json:"uri"`
	Range rng    `json:"range"`
}
type textID struct {
	URI string `json:"uri"`
}
type textPosition struct {
	TextDocument textID   `json:"textDocument"`
	Position     position `json:"position"`
}
type referenceParams struct {
	textPosition
	Context struct {
		IncludeDeclaration bool `json:"includeDeclaration"`
	} `json:"context"`
}
type textItem struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
	Text    string `json:"text"`
}
type openParams struct {
	TextDocument textItem `json:"textDocument"`
}
type changeParams struct {
	TextDocument   textID `json:"textDocument"`
	ContentChanges []struct {
		Text string `json:"text"`
	} `json:"contentChanges"`
}
type closeParams struct {
	TextDocument textID `json:"textDocument"`
}
type diagnostic struct {
	Range    rng    `json:"range"`
	Severity int    `json:"severity"`
	Code     string `json:"code"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}
type publishParams struct {
	URI         string       `json:"uri"`
	Diagnostics []diagnostic `json:"diagnostics"`
}

type server struct {
	mu             sync.Mutex
	analysisMu     sync.Mutex
	workspaceMu    sync.Mutex
	conn           jsonrpc2.Conn
	open           map[string][]byte
	roots          []string
	workspace      map[string]*index
	workspaceGen   uint64
	workspaceKey   string
	workspaceReady bool
	entries        map[string]*index
	good           map[string]*index
	published      map[string]bool
	generation     uint64
	timer          *time.Timer
	exit           chan struct{}
	shutdown       bool
	cancels        map[jsonrpc2.ID]context.CancelFunc
}

type stdio struct {
	io.Reader
	io.Writer
}

func (s stdio) Close() error {
	if c, ok := s.Reader.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Serve runs one server connection. Protocol bytes are written only to stdout;
// callers may use stderr for startup and compiler failures.
func Serve(in io.Reader, out io.Writer) error {
	conn := jsonrpc2.NewConn(jsonrpc2.NewStream(stdio{in, out}))
	s := &server{conn: conn, open: map[string][]byte{}, entries: map[string]*index{}, good: map[string]*index{}, published: map[string]bool{}, cancels: map[jsonrpc2.ID]context.CancelFunc{}, exit: make(chan struct{})}
	conn.Go(context.Background(), func(ctx context.Context, req *jsonrpc2.Request) (any, error) {
		if req.Method() == "textDocument/definition" || req.Method() == "textDocument/hover" || req.Method() == "textDocument/references" {
			queryCtx, cancel := context.WithCancel(jsonrpc2.DetachContext(ctx))
			id := req.ID()
			s.mu.Lock()
			s.cancels[id] = cancel
			s.mu.Unlock()
			jsonrpc2.Async(ctx)
			defer func() { s.mu.Lock(); delete(s.cancels, id); s.mu.Unlock(); cancel() }()
			return s.handle(queryCtx, req)
		}
		return s.handle(ctx, req)
	})
	select {
	case <-conn.Done():
	case <-s.exit:
		_ = conn.Close()
	}
	return conn.Err()
}

func (s *server) handle(ctx context.Context, req *jsonrpc2.Request) (any, error) {
	switch req.Method() {
	case "initialize":
		var p initializeParams
		if err := json.Unmarshal(req.Params(), &p); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.roots = initializeRoots(p)
		s.workspaceReady = false
		s.mu.Unlock()
		return map[string]any{"capabilities": map[string]any{"textDocumentSync": 1, "definitionProvider": true, "hoverProvider": true, "referencesProvider": true}, "serverInfo": map[string]string{"name": "fango"}}, nil
	case "initialized":
		return nil, nil
	case "$/cancelRequest":
		var p struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(req.Params(), &p); err == nil {
			if len(p.ID) > 0 && p.ID[0] == '"' {
				var id string
				if json.Unmarshal(p.ID, &id) == nil {
					s.cancelRequest(jsonrpc2.NewStringID(id))
				}
			}
			if len(p.ID) > 0 && p.ID[0] != '"' {
				var id int64
				if json.Unmarshal(p.ID, &id) == nil {
					s.cancelRequest(jsonrpc2.NewNumberID(id))
				}
			}
		}
		return nil, nil
	case "shutdown":
		s.mu.Lock()
		s.shutdown = true
		s.mu.Unlock()
		return nil, nil
	case "exit":
		close(s.exit)
		return nil, nil
	case "textDocument/didOpen":
		var p openParams
		if err := json.Unmarshal(req.Params(), &p); err != nil {
			return nil, err
		}
		if path, ok := uriPath(p.TextDocument.URI); ok {
			s.mu.Lock()
			s.open[path] = []byte(p.TextDocument.Text)
			s.schedule()
			s.mu.Unlock()
		}
		return nil, nil
	case "textDocument/didChange":
		var p changeParams
		if err := json.Unmarshal(req.Params(), &p); err != nil {
			return nil, err
		}
		if path, ok := uriPath(p.TextDocument.URI); ok && len(p.ContentChanges) > 0 {
			s.mu.Lock()
			s.open[path] = []byte(p.ContentChanges[len(p.ContentChanges)-1].Text)
			s.schedule()
			s.mu.Unlock()
		}
		return nil, nil
	case "textDocument/didClose":
		var p closeParams
		if err := json.Unmarshal(req.Params(), &p); err != nil {
			return nil, err
		}
		if path, ok := uriPath(p.TextDocument.URI); ok {
			s.mu.Lock()
			delete(s.open, path)
			delete(s.entries, path)
			s.good = selectGood(s.entries)
			s.schedule()
			s.mu.Unlock()
		}
		return nil, nil
	case "textDocument/didSave", "workspace/didChangeWatchedFiles":
		s.mu.Lock()
		s.schedule()
		s.mu.Unlock()
		return nil, nil
	case "workspace/didChangeWorkspaceFolders":
		var p workspaceFoldersChangeParams
		if err := json.Unmarshal(req.Params(), &p); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.roots = changedRoots(s.roots, p)
		s.schedule()
		s.mu.Unlock()
		return nil, nil
	case "textDocument/definition", "textDocument/hover", "textDocument/references":
		var p referenceParams
		if err := json.Unmarshal(req.Params(), &p); err != nil {
			return nil, err
		}
		path, ok := uriPath(p.TextDocument.URI)
		if !ok {
			return nil, nil
		}
		s.mu.Lock()
		idx := s.good[path]
		current, opened := s.open[path]
		good := s.good
		open := copyOpen(s.open)
		s.mu.Unlock()
		if idx == nil {
			s.analyze()
			s.mu.Lock()
			idx = s.good[path]
			current, opened = s.open[path]
			good = s.good
			open = copyOpen(s.open)
			s.mu.Unlock()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if idx == nil {
			return nil, nil
		}
		doc := idx.documents[path]
		if doc == nil {
			return nil, nil
		}
		file := doc.file
		if opened {
			file = source.NewFile(file.Name, current)
		}
		offset, valid := offsetAt(file, p.Position)
		if !valid {
			return nil, nil
		}
		chosen := occurrenceAt(doc, file, offset)
		if chosen == nil {
			return nil, nil
		}
		sym, exists := idx.symbols[chosen.target]
		if !exists {
			return nil, nil
		}
		if req.Method() == "textDocument/definition" {
			return location{URI: pathURI(sym.path), Range: spanRange(sym.span)}, nil
		}
		if req.Method() == "textDocument/references" {
			spelling := sym.span.File.Content[sym.span.Start:sym.span.End]
			workspace, err := s.workspaceIndexes(ctx, path, spelling)
			if err != nil {
				return nil, err
			}
			good = copyIndexes(good)
			for candidate, candidateIndex := range selectGood(workspace) {
				if good[candidate] == nil {
					good[candidate] = candidateIndex
				}
			}
			return references(ctx, good, open, chosen.target, sym, p.Context.IncludeDeclaration)
		}
		if sym.typeText == "" && sym.docs == "" {
			return nil, nil
		}
		var parts []string
		if sym.typeText != "" {
			parts = append(parts, "```fango\n"+sym.typeText+"\n```")
		}
		if sym.docs != "" {
			parts = append(parts, sym.docs)
		}
		return map[string]any{"contents": map[string]string{"kind": "markdown", "value": strings.Join(parts, "\n\n")}, "range": spanRange(chosen.span)}, nil
	default:
		if req.IsCall() {
			return nil, jsonrpc2.ErrMethodNotFound
		}
		return nil, nil
	}
}

func occurrenceAt(doc *documentIndex, current *source.File, offset int) *occurrence {
	var chosen *occurrence
	for n := range doc.uses {
		u := &doc.uses[n]
		if u.span.Start <= offset && offset < u.span.End && (chosen == nil || u.span.End-u.span.Start < chosen.span.End-chosen.span.Start) {
			chosen = u
		}
	}
	if chosen != nil && sameOccurrenceText(doc.file, current, chosen.span) {
		return chosen
	}
	return nil
}

func copyOpen(open map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(open))
	for path, content := range open {
		out[path] = content
	}
	return out
}

func copyIndexes(indexes map[string]*index) map[string]*index {
	out := make(map[string]*index, len(indexes))
	for path, idx := range indexes {
		out[path] = idx
	}
	return out
}

func sameOccurrenceText(indexed, current *source.File, span source.Span) bool {
	return span.Start >= 0 && span.End <= len(indexed.Content) && span.End <= len(current.Content) &&
		string(indexed.Content[span.Start:span.End]) == string(current.Content[span.Start:span.End]) &&
		positionAt(indexed, span.Start) == positionAt(current, span.Start) &&
		positionAt(indexed, span.End) == positionAt(current, span.End)
}

func references(ctx context.Context, good map[string]*index, open map[string][]byte, target string, definition symbol, includeDeclaration bool) ([]location, error) {
	out := []location{}
	seen := map[location]bool{}
	for path, idx := range good {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sym, ok := idx.symbols[target]
		if !ok || sym.path != definition.path || sym.span.Start != definition.span.Start || sym.span.End != definition.span.End {
			continue
		}
		doc := idx.documents[path]
		if doc == nil {
			continue
		}
		current := doc.file
		if content, opened := open[path]; opened {
			current = source.NewFile(doc.file.Name, content)
		}
		for _, use := range doc.uses {
			if use.target != target || !sameOccurrenceText(doc.file, current, use.span) {
				continue
			}
			if !includeDeclaration && path == definition.path && use.span.Start == definition.span.Start && use.span.End == definition.span.End {
				continue
			}
			loc := location{URI: pathURI(path), Range: spanRange(use.span)}
			if !seen[loc] {
				seen[loc] = true
				out = append(out, loc)
			}
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].URI != out[b].URI {
			return out[a].URI < out[b].URI
		}
		if out[a].Range.Start.Line != out[b].Range.Start.Line {
			return out[a].Range.Start.Line < out[b].Range.Start.Line
		}
		return out[a].Range.Start.Character < out[b].Range.Start.Character
	})
	return out, nil
}

func (s *server) cancelRequest(id jsonrpc2.ID) {
	s.mu.Lock()
	cancel := s.cancels[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *server) schedule() {
	s.generation++
	gen := s.generation
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(150*time.Millisecond, func() { s.analyzeGeneration(gen) })
}

func (s *server) analyze() { s.mu.Lock(); gen := s.generation; s.mu.Unlock(); s.analyzeGeneration(gen) }

func (s *server) analyzeGeneration(gen uint64) {
	s.analysisMu.Lock()
	defer s.analysisMu.Unlock()
	s.mu.Lock()
	if gen != s.generation {
		s.mu.Unlock()
		return
	}
	open := make(map[string][]byte, len(s.open))
	for k, v := range s.open {
		open[k] = append([]byte(nil), v...)
	}
	previous := s.published
	entries := make(map[string]*index, len(s.entries))
	for path, idx := range s.entries {
		if _, ok := s.open[path]; ok {
			entries[path] = idx
		}
	}
	s.mu.Unlock()
	diagnostics := map[string][]diagnostic{}
	for entry, content := range open {
		root := sourceRoot(entry, content)
		fresh := freshSources(root, entry, open)
		result, errs, internal := (&check.Session{LoadOptions: modules.LoadOptions{Root: root, Overlays: open, AllowBundledEntry: true}, FreshSources: fresh, AccumulateDiagnostics: true}).Compile(entry)
		if internal != nil {
			errs = append(errs, diag.Error{Title: "INTERNAL COMPILER ERROR", Body: internal.Error()})
		}
		for _, e := range errs {
			path := entry
			if e.Span.File != nil {
				path = sourcePath(root, e.Span.File.Name)
			}
			message := e.Body
			if len(e.Notes) > 0 {
				message += "\n\n" + strings.Join(e.Notes, "\n")
			}
			if message == "" {
				message = e.Title
			}
			var rangeValue rng
			if e.Span.File != nil {
				rangeValue = spanRange(e.Span)
			}
			item := diagnostic{Range: rangeValue, Severity: 1, Code: e.Title, Source: "fango", Message: message}
			found := false
			for _, prior := range diagnostics[path] {
				if prior == item {
					found = true
					break
				}
			}
			if !found {
				diagnostics[path] = append(diagnostics[path], item)
			}
		}
		if result != nil {
			entries[entry] = newIndex(root, result)
		}
	}
	s.mu.Lock()
	if gen != s.generation {
		s.mu.Unlock()
		return
	}
	s.entries = entries
	s.good = selectGood(entries)
	newPublished := map[string]bool{}
	for path := range diagnostics {
		newPublished[path] = true
	}
	for path := range open {
		newPublished[path] = true
	}
	s.published = newPublished
	s.mu.Unlock()
	for path := range previous {
		if !newPublished[path] {
			diagnostics[path] = []diagnostic{}
		}
	}
	for path := range newPublished {
		if diagnostics[path] == nil {
			diagnostics[path] = []diagnostic{}
		}
	}
	for path, items := range diagnostics {
		_ = s.conn.Notify(context.Background(), "textDocument/publishDiagnostics", publishParams{URI: pathURI(path), Diagnostics: items})
	}
}

// Each open entry contributes its last valid graph. Its own analysis wins for
// that file; imported files use a deterministic graph when several are open.
func selectGood(entries map[string]*index) map[string]*index {
	good := map[string]*index{}
	paths := make([]string, 0, len(entries))
	for path := range entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if entries[path].documents[path] != nil {
			good[path] = entries[path]
		}
	}
	for _, entry := range paths {
		for path := range entries[entry].documents {
			if good[path] == nil {
				good[path] = entries[entry]
			}
		}
	}
	return good
}

var headerLine = regexp.MustCompile(`(?m)^module\s+([A-Z][A-Za-z0-9_.]*)\b`)

func sourceRoot(path string, content []byte) string {
	root := filepath.Dir(path)
	match := headerLine.FindSubmatch(content)
	if match == nil {
		return root
	}
	parts := strings.Split(string(match[1]), ".")
	want := filepath.Join(append(parts[:len(parts)-1], parts[len(parts)-1]+".fango")...)
	if !strings.HasSuffix(path, string(filepath.Separator)+want) {
		return root
	}
	for range parts[:len(parts)-1] {
		root = filepath.Dir(root)
	}
	return root
}
func sourcePath(root, name string) string {
	if strings.HasPrefix(name, "<stdlib>/") {
		if lib, err := libroot.Root(); err == nil {
			return filepath.Join(lib, "stdlib", strings.TrimPrefix(name, "<stdlib>/"))
		}
	}
	return filepath.Clean(filepath.Join(root, filepath.FromSlash(name)))
}
func uriPath(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.Host != "" {
		return "", false
	}
	return filepath.Clean(filepath.FromSlash(u.Path)), true
}
func pathURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func offsetAt(f *source.File, p position) (int, bool) {
	if p.Line < 0 || p.Character < 0 {
		return 0, false
	}
	line := 0
	start := 0
	for line < p.Line && start < len(f.Content) {
		if f.Content[start] == '\n' {
			line++
		}
		start++
	}
	if line != p.Line {
		return 0, false
	}
	at := start
	units := 0
	for at < len(f.Content) && f.Content[at] != '\n' && f.Content[at] != '\r' && units < p.Character {
		r, size := utf8.DecodeRune(f.Content[at:])
		if size == 1 && r == utf8.RuneError {
			return 0, false
		}
		units += len(utf16.Encode([]rune{r}))
		at += size
	}
	if units != p.Character {
		return 0, false
	}
	return at, true
}
func positionAt(f *source.File, offset int) position {
	if offset < 0 {
		offset = 0
	}
	if offset > len(f.Content) {
		offset = len(f.Content)
	}
	line, start := 0, 0
	for n := 0; n < offset; n++ {
		if f.Content[n] == '\n' {
			line++
			start = n + 1
		}
	}
	return position{Line: line, Character: len(utf16.Encode([]rune(string(f.Content[start:offset]))))}
}
func spanRange(sp source.Span) rng {
	return rng{Start: positionAt(sp.File, sp.Start), End: positionAt(sp.File, sp.End)}
}
