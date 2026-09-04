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

When code and documentation disagree, determine deliberately whether the code
is wrong or the document is stale. Preserve the existing verification gates,
including the Core linter, interpreter/compiler differential suite, functional
tests, benchmarks, and `go vet`; update relevant goldens with intentional
language changes.
