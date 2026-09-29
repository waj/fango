# Fango for VS Code

Syntax highlighting, formatting, navigation, hover, and diagnostics for the [Fango](../../README.md) programming
language (`.fango` files).

## What's covered

- Keywords: `module`, `import`, `as`, `exposing`, `type`, `effect`, `abort`,
  `native`, `infixl`/`infixr`/`infix`, `if`/`then`/`else`, `case`/`of`, and
  `handle`/`resume` (plus the contextual `return` clause in handlers)
- Type classes: `class`, `instance`, `deriving (…)`, and `Ctx =>` constraints in signatures
- Comments: `--` line comments and nesting `{- … -}` block comments
- Strings with the exact Fango escape set (`\\ \" \n \t \r`; anything else is flagged as invalid)
- `native "…"` bodies with `$1`-style placeholders highlighted
- Type annotations, effect rows (`->{IO}`, `{Fail String | e}`), ADT declarations, list expressions and patterns (`[one, two | rest]`), qualified names (`List.range`), numeric literals, operator and fixity declarations (`(<+>) a b = …`, `infixl 6 (<+>)`), and user-declared operators
- Semicolon-separated statement bodies, such as `{ x -> print x; x + 1 }`
- Editing affordances: comment toggling, bracket matching/auto-closing, indent heuristics
- Formatting, by running `fango fmt` over the buffer
- Go to Definition for named declarations, imports, local bindings, and record fields
- Type and adjacent leading-comment hover for named symbols
- Live compiler errors from unsaved buffers

## Language server

The extension starts `fango lsp` when a `.fango` file opens. It uses the same
`fango.path` lookup described below for formatting. The server checks open
buffers and imported local modules, then updates errors after edits. Definition
and hover use the last valid analysis during a broken edit when the symbol at
the queried range still matches. It uses the existing `--` and `{- … -}`
comments immediately above a declaration as hover documentation.

The extension now needs its runtime dependency installed before using a local
symlink: run `npm install` in this directory. It remains plain JavaScript and
has no build step. Run `npm run test:extension` to check server startup wiring.

## Formatting

The extension registers a formatter for `.fango` files and turns on
`editor.formatOnSave` for them, so saving formats. Both are ordinary settings:
format manually with `Format Document` instead, or turn the default off with

```json
"[fango]": { "editor.formatOnSave": false }
```

It runs the `fango` executable, resolved in this order:

1. the `fango.path` setting, when set;
2. a `fango` built at the workspace root, so working on the compiler formats
   with the compiler you just built (`make build` produces it);
3. `fango` on `PATH`.

A buffer that does not parse is left exactly as it is — `fango fmt` declines
rather than guessing, which is what you want while a file is mid-edit — and the
reason goes to the `Fango` output channel rather than interrupting the save. A
missing executable is reported the same way.

The formatter continues to work through its existing provider; the language
server supplies navigation, hover, and diagnostics.

For grammar changes, run `npm install` and `npm run test:grammar` in this
directory. The development dependencies tokenize stdlib, testdata, and examples
with `vscode-textmate` and check braced-lambda, record, and ordinary pipe scopes.

## Install (local)

VS Code loads extensions from `~/.vscode/extensions`, so a symlink is enough:

```sh
ln -s "$(pwd)/editors/vscode" ~/.vscode/extensions/fango-lang
```

Run `npm install` in `editors/vscode`, then reload VS Code
(`Developer: Reload Window`) and open any `.fango` file.

Alternatively, package it with [`vsce`](https://github.com/microsoft/vscode-vsce):

```sh
cd editors/vscode && npx @vscode/vsce package
```

and install the resulting `.vsix` via `code --install-extension fango-lang-0.3.0.vsix`.
