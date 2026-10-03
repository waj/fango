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
  // Operation-free declarations end on this line, including at EOF.
  for (const sample of ["effect IO", "effect Marker a"]) {
    const line = grammar.tokenizeLine(sample);
    const scopes = index => line.tokens.find(t => t.startIndex <= index && t.endIndex > index).scopes;
    assert(scopes(0).includes("keyword.other.effect.fango"));
    assert(scopes(sample.indexOf(" ") + 1).includes("entity.name.type.effect.fango"));
    const next = grammar.tokenizeLine("value = 1", line.ruleStack);
    assert(!next.tokens.some(t => t.scopes.includes("entity.name.type.effect.fango")));
  }
  const sample = 'test "name" { value -> value |> finish }';
  const tokens = grammar.tokenizeLine(sample).tokens;
  const scopeAt = index => tokens.find(t => t.startIndex <= index && t.endIndex > index).scopes;
  assert(scopeAt(sample.indexOf("{")).includes("punctuation.section.braces.begin.fango"));
  assert(scopeAt(sample.indexOf("value")).includes("variable.parameter.fango"));
  assert(scopeAt(sample.indexOf("->")).includes("keyword.operator.arrow.fango"));
  assert(scopeAt(sample.indexOf("|>")).includes("keyword.operator.fango"));
  assert(!scopeAt(sample.indexOf("|>")).includes("keyword.operator.pipe.fango"));
  assert(!scopeAt(sample.lastIndexOf("value")).includes("variable.parameter.fango"));

  for (const [sample, count] of [
    ['f /abc/ // /a\\/b/ /[\\/]/ /\\\\/ /(?i)abc/', 6],
    ['x/y/z', 0], ['x / y / z', 0], ['x /y', 0],
    ['(/), (/=)', 0], ['x /= y && z /= w', 0],
    ['(/abc/) [/abc/,//]', 3], ['(//) (/^$/) (/=/)', 3], ['`/abc/`', 1],
    ['f /--{-"}/ -- comment', 1],
    ['f /abc', 0], ['f /*/', 1],
  ]) {
    const tokens = grammar.tokenizeLine(sample).tokens;
    const beginnings = tokens.filter(t => t.scopes.includes('string.regexp.fango') && t.scopes.includes('punctuation.definition.string.begin.fango'));
    assert.equal(beginnings.length, count, sample);
  }

  const sequence = 'apply { a b -> print a; b + 1 }';
  const sequenceTokens = grammar.tokenizeLine(sequence).tokens;
  const separator = sequenceTokens.find(t => t.startIndex <= sequence.indexOf(";") && t.endIndex > sequence.indexOf(";"));
  assert(separator.scopes.includes("punctuation.separator.statement.fango"));

  for (const [sample, member] of [
    ['{ x = 1 }', true],
    ['{ value | x = 1 }', true],
    ['Wrap { x = 1 }', true],
    ['Wrap { foo }', false],
    ['{ foo }', false],
  ]) {
    const tokens = grammar.tokenizeLine(sample).tokens;
    assert.equal(tokens.some(t => t.scopes.includes('variable.other.member.fango')), member, sample);
  }

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
  const scopedPragma = grammar.tokenizeLine('{-# scoped s #-}').tokens;
  assert(scopedPragma.some(t => t.scopes.includes('keyword.control.directive.fango')));
  assert(grammar.tokenizeLine('scoped = 1').tokens.every(t => !t.scopes.includes('keyword.control.directive.fango')));
  const sharedPragma = grammar.tokenizeLine('{-# shared-resource #-}').tokens;
  assert(!sharedPragma.some(t => t.scopes.includes('keyword.control.directive.fango')));
  assert(!grammar.tokenizeLine('{-# service #-}').tokens.some(t => t.scopes.includes('keyword.control.directive.fango')));
  assert(grammar.tokenizeLine('resource = 1').tokens.every(t => !t.scopes.includes('keyword.control.directive.fango')));
  const attribute = '#[Json.Key "foo", Json.Default `[1, 2]`]';
  const attributeTokens = grammar.tokenizeLine(attribute).tokens;
  const attributeScopeAt = index => attributeTokens.find(t => t.startIndex <= index && t.endIndex > index).scopes;
  assert(attributeScopeAt(0).includes('punctuation.definition.attribute.begin.fango'));
  assert(attributeScopeAt(attribute.length - 1).includes('punctuation.definition.attribute.end.fango'));
  assert(attributeScopeAt(attribute.indexOf('"foo"')).some(scope => scope.startsWith('string.')));
  const afterAttribute = grammar.tokenizeLine('#[Example.Tags ["a", "b"]] field : String').tokens;
  assert(!afterAttribute.find(t => t.startIndex <= 26 && t.endIndex > 26).scopes.includes('meta.attribute.fango'));
  const trailingField = '    , count : Int  #[Json.Default `7`]';
  const trailingTokens = grammar.tokenizeLine(trailingField).tokens;
  const trailingScopeAt = index => trailingTokens.find(t => t.startIndex <= index && t.endIndex > index).scopes;
  assert(!trailingScopeAt(trailingField.indexOf('count')).includes('meta.attribute.fango'));
  assert(trailingScopeAt(trailingField.indexOf('#[')).includes('punctuation.definition.attribute.begin.fango'));
  assert(trailingScopeAt(trailingField.length - 1).includes('punctuation.definition.attribute.end.fango'));
  const multilineAttribute = grammar.tokenizeLine('#[Json.Key "foo",');
  const attributeEnd = grammar.tokenizeLine('  Json.Default `0`] field : Int', multilineAttribute.ruleStack).tokens;
  assert(attributeEnd.some(t => t.scopes.includes('punctuation.definition.attribute.end.fango')));
  for (const witness of ['parse @Person input', 'parse @(List Person) input']) {
    const witnessTokens = grammar.tokenizeLine(witness).tokens;
    const at = witness.indexOf('@');
    assert(witnessTokens.find(t => t.startIndex <= at && t.endIndex > at).scopes.includes('keyword.operator.type-witness.fango'), witness);
  }
  const ordinaryAt = grammar.tokenizeLine('x @ y').tokens;
  assert(!ordinaryAt.some(t => t.scopes.includes('keyword.operator.type-witness.fango')));

  const quotation = 'build `show $(value)` `"`"`';
  const quotationTokens = grammar.tokenizeLine(quotation).tokens;
  const quoteScopeAt = index => quotationTokens.find(t => t.startIndex <= index && t.endIndex > index).scopes;
  assert(quoteScopeAt(quotation.indexOf('`')).includes('punctuation.definition.quote.begin.fango'));
  assert(quoteScopeAt(quotation.indexOf('$(')).includes('keyword.operator.splice.fango'));
  assert(quoteScopeAt(quotation.lastIndexOf('`')).includes('punctuation.definition.quote.end.fango'));
  assert(!quoteScopeAt(quotation.indexOf('show')).some(scope => scope.startsWith('string.')));
  assert(quoteScopeAt(quotation.indexOf('"')).some(scope => scope.startsWith('string.')));
  assert(!grammar.tokenizeLine('quote = 1').tokens.some(t => t.scopes.includes('keyword.control.fango')));

  const holeQuotation = '`$(`1`) + 2`';
  const holeTokens = grammar.tokenizeLine(holeQuotation).tokens;
  const delimiters = holeTokens.filter(t => t.scopes.some(scope => scope.startsWith('punctuation.definition.quote.')));
  assert.deepEqual(delimiters.map(t => t.scopes.at(-1)), [
    'punctuation.definition.quote.begin.fango',
    'punctuation.definition.quote.begin.fango',
    'punctuation.definition.quote.end.fango',
    'punctuation.definition.quote.end.fango',
  ]);
  let quotationStack = textmate.INITIAL;
  for (const line of ['code = `case True of', '    True -> "`" -- `', "    False -> '`' {- ` -}", '`', 'following = 1']) {
    const result = grammar.tokenizeLine(line, quotationStack);
    assert(!result.stoppedEarly);
    quotationStack = result.ruleStack;
    if (line === 'following = 1') assert(result.tokens.every(t => !t.scopes.includes('meta.quote.fango')));
  }

  // A handler's state may follow its subject on the `handle` line, or start
  // its own line after a block subject; `on` is a keyword wherever it sits.
  for (const handler of ['handle action() with state = 0 on', '    with state = 0 on']) {
    const handlerTokens = grammar.tokenizeLine(handler).tokens;
    const handlerScopeAt = index => handlerTokens.find(t => t.startIndex <= index && t.endIndex > index).scopes;
    assert(handlerScopeAt(handler.indexOf("with")).includes("keyword.control.with.fango"));
    assert(!handlerScopeAt(handler.indexOf("state")).some(scope => scope.startsWith("keyword.")));
    assert(handlerScopeAt(handler.lastIndexOf("on")).includes("keyword.control.fango"));
  }
  // An operation signature heads a handler clause group; it reads like an
  // effect declaration line, and a qualified operation may carry one.
  for (const line of ["        fail : ParseError -> a", "        Fail.fail : IoError -> a", "        tick : () ->{Clock Simulation} Int"]) {
    const tokens = grammar.tokenizeLine(line).tokens;
    const scopeAt = index => tokens.find(t => t.startIndex <= index && t.endIndex > index).scopes;
    const head = line.indexOf(line.trimStart()[0]);
    assert(scopeAt(head).includes("entity.name.function.fango"));
    assert(scopeAt(line.indexOf(":")).includes("keyword.operator.type-annotation.fango"));
    assert(scopeAt(line.indexOf("->") + 1).includes("keyword.operator.arrow.fango") || scopeAt(line.indexOf("->")).some(s => s.startsWith("keyword.operator")));
  }
  const ownLine = grammar.tokenizeLine('    on').tokens;
  assert(ownLine.find(t => t.startIndex <= 4 && t.endIndex > 4).scopes.includes("keyword.control.fango"));
  const binding = grammar.tokenizeLine('    with x = 1').tokens;
  assert(!binding.find(t => t.startIndex <= 4 && t.endIndex > 4).scopes.includes("keyword.control.with.fango"));

  // A `use` block item is a keyword at the start of an item; its binder
  // patterns are parameters up to the `<-`.
  for (const item of ['    use Client.run', '    use (a, b) <- pair first', 'x = twice { use n <- pair 1; use run; n }']) {
    const itemTokens = grammar.tokenizeLine(item).tokens;
    const itemScopeAt = index => itemTokens.find(t => t.startIndex <= index && t.endIndex > index).scopes;
    assert(itemScopeAt(item.indexOf("use")).includes("keyword.control.use.fango"));
    if (item.includes("<-")) {
      assert(itemScopeAt(item.indexOf("<-")).includes("keyword.operator.arrow.fango"));
      if (item.includes("n <-")) assert(itemScopeAt(item.indexOf("n <-")).includes("variable.parameter.fango"));
    }
    if (item.includes("; use run")) {
      assert(itemScopeAt(item.indexOf("use run")).includes("keyword.control.use.fango"));
    }
  }
  // `with` outside handler state is an ordinary name, and `use` is reserved
  // even where it cannot start an item.
  const withFunction = grammar.tokenizeLine('    with f x = f x').tokens;
  assert(!withFunction.find(t => t.startIndex <= 4 && t.endIndex > 4).scopes.some(scope => scope.startsWith("keyword.")));
  const misplacedUse = grammar.tokenizeLine('x = f use').tokens;
  assert(misplacedUse.find(t => t.startIndex <= 6 && t.endIndex > 6).scopes.includes("keyword.control.fango"));

  // Declaration and handler-clause heads keep their scopes when later lines
  // outdent from the first item of an indented group.
  for (const line of ["        x = 1", "      y = x + 2"]) {
    const tokens = grammar.tokenizeLine(line).tokens;
    const head = line.indexOf(line.trimStart()[0]);
    assert(tokens.find(t => t.startIndex <= head && t.endIndex > head).scopes.includes("variable.other.definition.fango"));
  }
  for (const line of ["        return value -> value", "      return value -> value"]) {
    const tokens = grammar.tokenizeLine(line).tokens;
    const head = line.indexOf("return");
    assert(tokens.find(t => t.startIndex <= head && t.endIndex > head).scopes.includes("keyword.control.return.fango"));
  }
  // A class block mixes signatures with default implementations; a default
  // line names a function, as an instance method does.
  let classStack = textmate.INITIAL;
  for (const line of ["class Size a", "    size : a -> Int", "    isEmpty x = size x == 0"]) {
    const result = grammar.tokenizeLine(line, classStack);
    assert(!result.stoppedEarly);
    if (line.includes("isEmpty")) {
      const head = line.indexOf("isEmpty");
      assert(result.tokens.find(t => t.startIndex <= head && t.endIndex > head).scopes.includes("entity.name.function.fango"));
    }
    classStack = result.ruleStack;
  }
  let parenthesizedStack = textmate.INITIAL;
  for (const line of ["main =", "    foo { _ ->", "        foo", "    bar", "    }"]) {
    const result = grammar.tokenizeLine(line, parenthesizedStack);
    assert(!result.stoppedEarly);
    parenthesizedStack = result.ruleStack;
  }
  let incompleteBlockStack = textmate.INITIAL;
  for (const line of ["main =", "    foo { _ ->", "    value = x", "        bar", "    }"]) {
    const result = grammar.tokenizeLine(line, incompleteBlockStack);
    assert(!result.stoppedEarly);
    incompleteBlockStack = result.ruleStack;
  }

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
  console.log(`Tokenized ${files} Fango files; lambda, record, and pipe scopes passed.`);
  registry.dispose();
}

main().catch(error => { console.error(error); process.exitCode = 1; });
