package lsp

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/source"
	"go.lsp.dev/jsonrpc2"
)

func TestNavigationInOpenedBundledModule(t *testing.T) {
	root, err := libroot.Root()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "stdlib", "Http", "Server", "Route.fango")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file := source.NewFile(path, data)
	use := strings.Index(string(data), "else lookup name rest")
	definition := strings.Index(string(data), "lookup : String")
	if use < 0 || definition < 0 {
		t.Fatal("Route lookup fixture changed")
	}
	use += len("else ")
	clientSide, serverSide := net.Pipe()
	serverDone := make(chan error, 1)
	go func() { serverDone <- Serve(serverSide, serverSide) }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	updates := make(chan publishParams, 16)
	conn.Go(ctx, func(_ context.Context, req *jsonrpc2.Request) (any, error) {
		if req.Method() == "textDocument/publishDiagnostics" {
			var p publishParams
			if err := json.Unmarshal(req.Params(), &p); err != nil {
				return nil, err
			}
			updates <- p
		}
		return nil, nil
	})
	defer conn.Close()
	if _, err := conn.Call(ctx, "initialize", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	uri := pathURI(path)
	if err := conn.Notify(ctx, "textDocument/didOpen", openParams{TextDocument: textItem{URI: uri, Version: 1, Text: string(data)}}); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-updates:
		if p.URI != uri || len(p.Diagnostics) != 0 {
			t.Fatalf("bundled diagnostics: %#v", p)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var target location
	query := textPosition{TextDocument: textID{URI: uri}, Position: positionAt(file, use)}
	if _, err := conn.Call(ctx, "textDocument/definition", query, &target); err != nil {
		t.Fatal(err)
	}
	if target.URI != uri || target.Range.Start != positionAt(file, definition) {
		t.Fatalf("definition = %#v", target)
	}
	var references []location
	refs := referenceParams{textPosition: query}
	refs.Context.IncludeDeclaration = true
	if _, err := conn.Call(ctx, "textDocument/references", refs, &references); err != nil {
		t.Fatal(err)
	}
	foundDefinition := false
	for _, ref := range references {
		if ref.URI == target.URI && ref.Range == target.Range {
			foundDefinition = true
		}
	}
	if !foundDefinition || len(references) < 2 {
		t.Fatalf("bundled references = %#v", references)
	}
	if _, err := conn.Call(ctx, "shutdown", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := conn.Notify(ctx, "exit", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestProtocolUnsavedNavigationAndDiagnostics(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Main.fango")
	good := "module Main exposing (main)\n\n-- A documented value.\nvalue : Int\nvalue = 3\n\nmain = value\n"
	if err := os.WriteFile(path, []byte("module Main exposing (main)\nmain = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clientSide, serverSide := net.Pipe()
	serverDone := make(chan error, 1)
	go func() { serverDone <- Serve(serverSide, serverSide) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	updates := make(chan publishParams, 16)
	conn.Go(ctx, func(_ context.Context, req *jsonrpc2.Request) (any, error) {
		if req.Method() == "textDocument/publishDiagnostics" {
			var p publishParams
			if err := json.Unmarshal(req.Params(), &p); err != nil {
				return nil, err
			}
			updates <- p
		}
		return nil, nil
	})
	defer conn.Close()
	var initialized map[string]any
	if _, err := conn.Call(ctx, "initialize", map[string]any{}, &initialized); err != nil {
		t.Fatal(err)
	}
	uri := pathURI(path)
	if err := conn.Notify(ctx, "textDocument/didOpen", openParams{TextDocument: textItem{URI: uri, Version: 1, Text: good}}); err != nil {
		t.Fatal(err)
	}
	waitDiagnostics(t, ctx, updates, uri, 0)
	var definition location
	query := textPosition{TextDocument: textID{URI: uri}, Position: position{Line: 6, Character: 9}}
	if _, err := conn.Call(ctx, "textDocument/definition", query, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.URI != uri || definition.Range.Start.Line != 3 {
		t.Fatalf("definition = %#v", definition)
	}
	var hover struct {
		Contents struct {
			Value string `json:"value"`
		} `json:"contents"`
	}
	if _, err := conn.Call(ctx, "textDocument/hover", query, &hover); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hover.Contents.Value, "value : Int") || !strings.Contains(hover.Contents.Value, "A documented value") {
		t.Fatalf("hover = %#v", hover)
	}
	bad := strings.TrimSuffix(good, "\n") + " +\n"
	change := changeParams{TextDocument: textID{URI: uri}, ContentChanges: []struct {
		Text string `json:"text"`
	}{{Text: bad}}}
	if err := conn.Notify(ctx, "textDocument/didChange", change); err != nil {
		t.Fatal(err)
	}
	waitDiagnostics(t, ctx, updates, uri, 1)
	definition = location{}
	if _, err := conn.Call(ctx, "textDocument/definition", query, &definition); err != nil {
		t.Fatal(err)
	}
	if definition.Range.Start.Line != 3 {
		t.Fatalf("last valid definition = %#v", definition)
	}
	change.ContentChanges[0].Text = good
	if err := conn.Notify(ctx, "textDocument/didChange", change); err != nil {
		t.Fatal(err)
	}
	waitDiagnostics(t, ctx, updates, uri, 0)
	if _, err := conn.Call(ctx, "shutdown", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := conn.Notify(ctx, "exit", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestProtocolReferencesAcrossOpenGraphs(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "Main.fango")
	libPath := filepath.Join(root, "Lib.fango")
	main := "module Main exposing (main)\nimport Lib exposing (value)\nmain = value\n"
	lib := "module Lib exposing (value)\nvalue = 1\nother = value\n"
	for path, data := range map[string]string{mainPath: main, libPath: lib} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	clientSide, serverSide := net.Pipe()
	serverDone := make(chan error, 1)
	go func() { serverDone <- Serve(serverSide, serverSide) }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	updates := make(chan publishParams, 16)
	conn.Go(ctx, func(_ context.Context, req *jsonrpc2.Request) (any, error) {
		if req.Method() == "textDocument/publishDiagnostics" {
			var p publishParams
			if err := json.Unmarshal(req.Params(), &p); err != nil {
				return nil, err
			}
			updates <- p
		}
		return nil, nil
	})
	defer conn.Close()
	var initialized struct {
		Capabilities struct {
			ReferencesProvider bool `json:"referencesProvider"`
		} `json:"capabilities"`
	}
	if _, err := conn.Call(ctx, "initialize", map[string]any{}, &initialized); err != nil {
		t.Fatal(err)
	}
	if !initialized.Capabilities.ReferencesProvider {
		t.Fatal("references not advertised")
	}
	for _, item := range []struct{ path, data string }{{mainPath, main}, {libPath, lib}} {
		if err := conn.Notify(ctx, "textDocument/didOpen", openParams{TextDocument: textItem{URI: pathURI(item.path), Version: 1, Text: item.data}}); err != nil {
			t.Fatal(err)
		}
		waitDiagnostics(t, ctx, updates, pathURI(item.path), 0)
	}
	query := referenceParams{textPosition: textPosition{TextDocument: textID{URI: pathURI(libPath)}, Position: position{Line: 1, Character: 1}}}
	check := func(include bool, want ...location) {
		t.Helper()
		query.Context.IncludeDeclaration = include
		for {
			var got []location
			if _, err := conn.Call(ctx, "textDocument/references", query, &got); err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(got, want) {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("references(includeDeclaration=%v) = %#v, want %#v", include, got, want)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	mainExposing := location{URI: pathURI(mainPath), Range: rng{Start: position{Line: 1, Character: 21}, End: position{Line: 1, Character: 26}}}
	mainUse := location{URI: pathURI(mainPath), Range: rng{Start: position{Line: 2, Character: 7}, End: position{Line: 2, Character: 12}}}
	libExposing := location{URI: pathURI(libPath), Range: rng{Start: position{Line: 0, Character: 21}, End: position{Line: 0, Character: 26}}}
	libDefinition := location{URI: pathURI(libPath), Range: rng{Start: position{Line: 1, Character: 0}, End: position{Line: 1, Character: 5}}}
	libUse := location{URI: pathURI(libPath), Range: rng{Start: position{Line: 2, Character: 8}, End: position{Line: 2, Character: 13}}}
	check(false, libExposing, libUse, mainExposing, mainUse)
	check(true, libExposing, libDefinition, libUse, mainExposing, mainUse)
	change := changeParams{TextDocument: textID{URI: pathURI(mainPath)}, ContentChanges: []struct {
		Text string `json:"text"`
	}{{Text: main + "another = value\n"}}}
	if err := conn.Notify(ctx, "textDocument/didChange", change); err != nil {
		t.Fatal(err)
	}
	waitDiagnostics(t, ctx, updates, pathURI(mainPath), 0)
	mainAnother := location{URI: pathURI(mainPath), Range: rng{Start: position{Line: 3, Character: 10}, End: position{Line: 3, Character: 15}}}
	check(false, libExposing, libUse, mainExposing, mainUse, mainAnother)
	change.ContentChanges[0].Text = main + "another = value +\n"
	if err := conn.Notify(ctx, "textDocument/didChange", change); err != nil {
		t.Fatal(err)
	}
	waitAnyDiagnostics(t, ctx, updates, pathURI(mainPath))
	check(false, libExposing, libUse, mainExposing, mainUse, mainAnother)
	bad := "?\n" + main
	change.ContentChanges[0].Text = bad
	if err := conn.Notify(ctx, "textDocument/didChange", change); err != nil {
		t.Fatal(err)
	}
	waitAnyDiagnostics(t, ctx, updates, pathURI(mainPath))
	check(false, libExposing, libUse)
	if err := conn.Notify(ctx, "textDocument/didClose", closeParams{TextDocument: textID{URI: pathURI(mainPath)}}); err != nil {
		t.Fatal(err)
	}
	check(false, libExposing, libUse, mainExposing, mainUse)
	if _, err := conn.Call(ctx, "shutdown", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := conn.Notify(ctx, "exit", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestWorkspaceReferencesInUnopenedModule(t *testing.T) {
	root := t.TempDir()
	otherRoot := t.TempDir()
	libPath := filepath.Join(root, "Lib.fango")
	usePath := filepath.Join(root, "App", "Use.fango")
	if err := os.Mkdir(filepath.Dir(usePath), 0o755); err != nil {
		t.Fatal(err)
	}
	lib := "module Lib exposing (value)\nvalue = 1\n"
	use := "module App.Use exposing (result)\nimport Lib exposing (value)\nresult = value\n"
	for path, data := range map[string]string{libPath: lib, usePath: use} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	otherLib := filepath.Join(otherRoot, "Lib.fango")
	otherUse := filepath.Join(otherRoot, "App", "Use.fango")
	if err := os.Mkdir(filepath.Dir(otherUse), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{otherLib: lib, otherUse: use} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	clientSide, serverSide := net.Pipe()
	serverDone := make(chan error, 1)
	go func() { serverDone <- Serve(serverSide, serverSide) }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	updates := make(chan publishParams, 16)
	conn.Go(ctx, func(_ context.Context, req *jsonrpc2.Request) (any, error) {
		if req.Method() == "textDocument/publishDiagnostics" {
			var p publishParams
			if err := json.Unmarshal(req.Params(), &p); err != nil {
				return nil, err
			}
			updates <- p
		}
		return nil, nil
	})
	defer conn.Close()
	if _, err := conn.Call(ctx, "initialize", map[string]any{"workspaceFolders": []map[string]string{{"uri": pathURI(root)}, {"uri": pathURI(otherRoot)}}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := conn.Notify(ctx, "textDocument/didOpen", openParams{TextDocument: textItem{URI: pathURI(libPath), Version: 1, Text: lib}}); err != nil {
		t.Fatal(err)
	}
	waitDiagnostics(t, ctx, updates, pathURI(libPath), 0)
	query := referenceParams{textPosition: textPosition{TextDocument: textID{URI: pathURI(libPath)}, Position: position{Line: 1, Character: 1}}}
	libExpose := location{URI: pathURI(libPath), Range: rng{Start: position{Line: 0, Character: 21}, End: position{Line: 0, Character: 26}}}
	useImport := location{URI: pathURI(usePath), Range: rng{Start: position{Line: 1, Character: 21}, End: position{Line: 1, Character: 26}}}
	useExpr := location{URI: pathURI(usePath), Range: rng{Start: position{Line: 2, Character: 9}, End: position{Line: 2, Character: 14}}}
	check := func(want ...location) {
		t.Helper()
		for {
			var got []location
			if _, err := conn.Call(ctx, "textDocument/references", query, &got); err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(got, want) {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("workspace references = %#v, want %#v", got, want)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	check(useImport, useExpr, libExpose)
	if err := os.WriteFile(usePath, []byte("module App.Use exposing (result)\nresult = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := conn.Notify(ctx, "workspace/didChangeWatchedFiles", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	check(libExpose)
	if err := os.WriteFile(usePath, []byte(use), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := conn.Notify(ctx, "workspace/didChangeWatchedFiles", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	check(useImport, useExpr, libExpose)
	if _, err := conn.Call(ctx, "shutdown", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := conn.Notify(ctx, "exit", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func waitAnyDiagnostics(t *testing.T, ctx context.Context, updates <-chan publishParams, uri string) {
	t.Helper()
	for {
		select {
		case p := <-updates:
			if p.URI == uri && len(p.Diagnostics) > 0 {
				return
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func waitDiagnostics(t *testing.T, ctx context.Context, updates <-chan publishParams, uri string, count int) {
	t.Helper()
	for {
		select {
		case p := <-updates:
			if p.URI == uri && len(p.Diagnostics) == count {
				return
			}
		case <-ctx.Done():
			t.Fatalf("diagnostics: %v", ctx.Err())
		}
	}
}
