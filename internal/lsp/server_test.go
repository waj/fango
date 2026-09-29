package lsp

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
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
	path := filepath.Join(root, "stdlib", "Http", "Route.fango")
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
