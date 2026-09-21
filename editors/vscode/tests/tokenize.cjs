const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const textmate = require("vscode-textmate");
const oniguruma = require("vscode-oniguruma");

async function main() {
  const wasm = fs.readFileSync(require.resolve("vscode-oniguruma/release/onig.wasm"));
  await oniguruma.loadWASM(wasm.buffer.slice(wasm.byteOffset, wasm.byteOffset + wasm.byteLength));
  const grammarPath = path.resolve(__dirname, "../syntaxes/fango.tmLanguage.json");
  const registry = new textmate.Registry({
    onigLib: Promise.resolve({
      createOnigScanner: patterns => new oniguruma.OnigScanner(patterns),
      createOnigString: value => new oniguruma.OnigString(value),
    }),
    loadGrammar: async () => textmate.parseRawGrammar(fs.readFileSync(grammarPath, "utf8"), grammarPath),
  });
  const grammar = await registry.loadGrammar("source.fango");
  const sample = 'test "name" \\value -> value |> finish';
  const tokens = grammar.tokenizeLine(sample).tokens;
  const scopeAt = index => tokens.find(t => t.startIndex <= index && t.endIndex > index).scopes;
  assert(scopeAt(sample.indexOf("\\")).includes("keyword.operator.lambda.fango"));
  assert(scopeAt(sample.indexOf("value")).includes("variable.parameter.fango"));
  assert(scopeAt(sample.indexOf("->")).includes("keyword.operator.arrow.fango"));
  assert(scopeAt(sample.indexOf("|>")).includes("keyword.operator.fango"));
  assert(!scopeAt(sample.indexOf("|>")).includes("keyword.operator.pipe.fango"));
  assert(!scopeAt(sample.lastIndexOf("value")).includes("variable.parameter.fango"));

  const sequence = 'apply \\a b -> print a; b + 1';
  const sequenceTokens = grammar.tokenizeLine(sequence).tokens;
  const separator = sequenceTokens.find(t => t.startIndex <= sequence.indexOf(";") && t.endIndex > sequence.indexOf(";"));
  assert(separator.scopes.includes("punctuation.separator.statement.fango"));

  // A row literal is an effect row wherever it stands: the grammar's row rule
  // is not anchored to an arrow, so a row filling a type argument gets the
  // same scopes as one after `->`.
  const rowArgument = 'listen : Source {Console, Fail String | e}';
  const rowTokens = grammar.tokenizeLine(rowArgument).tokens;
  const rowScopeAt = index => rowTokens.find(t => t.startIndex <= index && t.endIndex > index).scopes;
  assert(rowScopeAt(rowArgument.indexOf("Source")).includes("entity.name.type.fango"));
  assert(rowScopeAt(rowArgument.indexOf("{")).includes("punctuation.definition.effect-row.begin.fango"));
  assert(rowScopeAt(rowArgument.indexOf("Console")).includes("entity.name.type.effect.fango"));
  assert(rowScopeAt(rowArgument.indexOf("|")).includes("keyword.operator.pipe.fango"));
  assert(rowScopeAt(rowArgument.indexOf("}")).includes("punctuation.definition.effect-row.end.fango"));

  const pragma = grammar.tokenizeLine('{-# resource #-}').tokens;
  assert(pragma.some(t => t.scopes.includes('keyword.control.directive.fango')));
  assert(grammar.tokenizeLine('resource = 1').tokens.every(t => !t.scopes.includes('keyword.control.directive.fango')));

  const root = path.resolve(__dirname, "../../..");
  let files = 0;
  function visit(dir) {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      if (entry.name.startsWith(".")) continue;
      const file = path.join(dir, entry.name);
      if (entry.isDirectory()) visit(file);
      else if (entry.name.endsWith(".fango")) {
        let stack = textmate.INITIAL;
        for (const line of fs.readFileSync(file, "utf8").split(/\r?\n/)) {
          const result = grammar.tokenizeLine(line, stack);
          assert(!result.stoppedEarly, `Tokenization stopped in ${file}`);
          stack = result.ruleStack;
        }
        files++;
      }
    }
  }
  for (const dir of ["stdlib", "testdata", "examples"]) visit(path.join(root, dir));
  console.log(`Tokenized ${files} Fango files; trailing-lambda and pipe scopes passed.`);
  registry.dispose();
}

main().catch(error => { console.error(error); process.exitCode = 1; });
