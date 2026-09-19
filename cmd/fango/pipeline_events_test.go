package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCompilationSessionStageEvents(t *testing.T) {
	d := t.TempDir()
	entry := filepath.Join(d, "Main.fango")
	if err := os.WriteFile(entry, []byte("main = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var events []stageEvent
	session := &compilationSession{observe: func(event stageEvent) { events = append(events, event) }}
	var stderr bytes.Buffer
	if _, _, ok := emitProjectManifestSession(entry, false, &stderr, session); !ok {
		t.Fatalf("cold compile failed: %s", stderr.String())
	}
	want := map[string]bool{
		"parse":         false,
		"resolve":       false,
		"check":         false,
		"elaborate":     false,
		"semantic-lint": false,
		"lowering":      false,
		"emission":      false,
	}
	for _, event := range events {
		if event.Owner == "" {
			t.Fatalf("event has no owner: %#v", event)
		}
		if _, ok := want[event.Stage]; ok {
			want[event.Stage] = true
		}
	}
	for stage, seen := range want {
		if !seen {
			t.Errorf("missing %s event in %#v", stage, events)
		}
	}

	// A second invocation rediscovers and revalidates the graph, then serves
	// every owner from its artifacts: no compiler stage runs again.
	events = nil
	if _, _, ok := emitProjectManifestSession(entry, false, &stderr, session); !ok {
		t.Fatalf("cached compile failed: %s", stderr.String())
	}
	hits := 0
	for _, event := range events {
		switch event.Stage {
		case "parse", "check", "elaborate", "semantic-lint", "lowering", "emission":
			t.Fatalf("a cached build repeated %s for %s: %#v", event.Stage, event.Owner, events)
		case "checked-cache-hit", "emitted-cache-hit":
			hits++
		}
		if event.Owner == "" {
			t.Fatalf("event has no owner: %#v", event)
		}
	}
	if hits == 0 {
		t.Fatalf("cached build reused nothing: %#v", events)
	}
}

func TestRepeatedDiscoveryUsesPersistentParsedUnits(t *testing.T) {
	d := t.TempDir()
	entry := filepath.Join(d, "Main.fango")
	if err := os.WriteFile(entry, []byte("main = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	countParses := func() int {
		count := 0
		session := &compilationSession{observe: func(event stageEvent) {
			if event.Stage == "parse" {
				count++
			}
		}}
		var stderr bytes.Buffer
		if _, _, _, _, _, ok := compileFileGraphSession(entry, &stderr, session); !ok {
			t.Fatalf("compile failed: %s", stderr.String())
		}
		return count
	}
	if first, second := countParses(), countParses(); first == 0 || second != 0 {
		t.Fatalf("parse counts = %d then %d", first, second)
	}
}
