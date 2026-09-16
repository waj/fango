# Repository instructions

Before architectural or implementation work, read the `doc/design.md` and
`doc/roadmap.md` entry pages, then follow only task-relevant topic links.
For user-visible changes, consult the relevant `doc/reference.md` topics.
Search headings with `rg` before reading whole topic files.

Update documentation with the implementation: reference owns behavior and
diagnostics, design owns architecture and invariants, roadmap owns unfinished
work. Keep design/reference limited to implemented behavior. Edit the authoritative
explanation rather than appending change summaries; link instead of duplicating,
and keep examples only for distinct rules.
Keep entry pages as navigation; split growing topics at coherent boundaries
and update links. Remove completed roadmap work after promoting durable results
into design/reference. Do not add implementation diaries or completed plan files;
Git history is the archive.

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
