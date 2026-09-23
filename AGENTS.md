# Repository instructions

Run Python helpers with `python3` from the Nix development shell (`nix develop`
or the direnv environment). The macOS `/usr/bin/python3` stub is not a usable
fallback when Python is absent from the shell.

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
and update links. Promote completed roadmap work into design/reference, then
remove its roadmap entry or mark it `DONE` under the milestone rules below.
Do not add implementation diaries or completed plan files; Git history is the
archive.

Roadmap milestone IDs, numbers, and titles are stable identifiers. Do not rename,
renumber, or repurpose existing stages unless the user explicitly requests it.
Adding, splitting, reordering, or removing work must not change the identities of
other stages. Allocate unused IDs or new substage suffixes for new work; never
reuse an ID retired by completion or removal. Do not delete unfinished milestones
without an explicit user request. Completed milestones may be removed or marked
`DONE`; when retaining one, put the status below its unchanged heading so its
title and anchor stay stable. Update dependency tables and links when a milestone
is removed, directing references to the implemented design/reference contract
where appropriate. Leave numbering gaps rather than compacting the remaining
stages, and check Git history before allocating an ID that may have been retired.

The VS Code extension in `editors/vscode/` is part of the language's
user-visible surface. When changing the surface syntax — keywords, operators,
literal forms, comment syntax, or declaration shapes (see `internal/token/`,
`internal/lexer/`, `internal/parser/`) — update the TextMate grammar in
`editors/vscode/syntaxes/fango.tmLanguage.json` (and, when comment or bracket
behavior changes, `editors/vscode/language-configuration.json`) in the same
change. Verify with `make test-grammar`, which tokenizes every `.fango` file
under stdlib, testdata, and examples with `vscode-textmate`; add a case to
`editors/vscode/tests/tokenize.cjs` for the new syntax. The grammar's regexes
encode exact lexer rules (escape set, float forms, reserved words), so keep them
in lockstep rather than approximating.

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
