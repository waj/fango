# Repository instructions

Before architectural or implementation work, read `doc/design.md` and
`doc/roadmap.md`. Before changing syntax, commands, diagnostics, or other
user-visible behavior, also read `doc/reference.md`.

Update the living documentation in the same change as the implementation:

- architecture and invariants belong in `doc/design.md`;
- implemented syntax, behavior, diagnostics, and commands belong in
  `doc/reference.md`;
- priorities, unfinished work, and open decisions belong in `doc/roadmap.md`.

Keep design and reference limited to implemented behavior. Put speculation in
the roadmap. When roadmap work is complete, promote durable results into design
or reference and remove the completed item; do not create implementation
diaries or completed plan files. Git history is the archive.

The VS Code extension in `editors/vscode/` is part of the language's
user-visible surface. When changing the surface syntax — keywords, operators,
literal forms, comment syntax, or declaration shapes (see `internal/token/`,
`internal/lexer/`, `internal/parser/`) — update the TextMate grammar in
`editors/vscode/syntaxes/fango.tmLanguage.json` (and, when comment or bracket
behavior changes, `editors/vscode/language-configuration.json`) in the same
change. Verify by tokenizing representative `.fango` files (stdlib, testdata,
and examples exercising the new syntax) with `vscode-textmate`; the grammar's
regexes encode exact lexer rules (escape set, float forms, reserved words), so
keep them in lockstep rather than approximating.

When code and documentation disagree, determine deliberately whether the code
is wrong or the document is stale. Preserve the existing verification gates,
including the Core linter, interpreter/compiler differential suite, functional
tests, benchmarks, and `go vet`; update relevant goldens with intentional
language changes.

Do not run the benchmarks during ordinary development. `make test` and `make ci`
already exclude them, and `make test-perf` measures elapsed time against
thresholds recorded on one machine, so under a normal working load it reports
regressions that are contention or a cold cache rather than the change. Check
only that they still build, with `go vet ./benchmarks`. Run `make test-perf` on
an otherwise idle machine, and only when the change is meant to move
performance or could plausibly affect it.
