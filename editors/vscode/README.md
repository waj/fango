# Fango for VS Code

Syntax highlighting for the [Fango](../../README.md) programming language (`.fango` files).

## What's covered

- Keywords: `module`, `import`, `as`, `exposing`, `type`, `effect`, `native`, `infixl`/`infixr`/`infix`, `if`/`then`/`else`, `case`/`of`, `handle`/`resume` (plus the contextual `return` clause in handlers)
- Type classes: `class`, `instance`, `deriving (…)`, and `Ctx =>` constraints in signatures
- Comments: `--` line comments and nesting `{- … -}` block comments
- Strings with the exact Fango escape set (`\\ \" \n \t \r`; anything else is flagged as invalid)
- `native "…"` bodies with `$1`-style placeholders highlighted
- Type annotations, effect rows (`->{IO}`, `{Fail String | e}`), ADT declarations, qualified names (`List.range`), numeric literals, operator and fixity declarations (`(<+>) a b = …`, `infixl 6 (<+>)`), and user-declared operators
- Editing affordances: comment toggling, bracket matching/auto-closing, indent heuristics

## Install (local)

VS Code loads extensions from `~/.vscode/extensions`, so a symlink is enough:

```sh
ln -s "$(pwd)/editors/vscode" ~/.vscode/extensions/fango-lang
```

Then reload VS Code (`Developer: Reload Window`) and open any `.fango` file.

Alternatively, package it with [`vsce`](https://github.com/microsoft/vscode-vsce):

```sh
cd editors/vscode && npx @vscode/vsce package
```

and install the resulting `.vsix` via `code --install-extension fango-lang-0.1.0.vsix`.
