const assert = require("node:assert/strict");
const Module = require("node:module");
const path = require("node:path");

let launched;
const document = {
  languageId: "fango",
  uri: { scheme: "file", fsPath: "/project/Main.fango" },
};
const folder = { uri: { fsPath: "/project" } };
const watcher = { dispose() {} };
const vscode = {
  RelativePattern: class { constructor(base, pattern) { this.base = base; this.pattern = pattern; } },
  workspace: {
    textDocuments: [document],
    getWorkspaceFolder: () => folder,
    getConfiguration: () => ({ get: () => "bin/fango" }),
    createFileSystemWatcher: () => watcher,
    onDidOpenTextDocument: () => ({ dispose() {} }),
    onDidChangeConfiguration: () => ({ dispose() {} }),
  },
  window: { createOutputChannel: () => ({ appendLine() {} }) },
  languages: { registerDocumentFormattingEditProvider: () => ({ dispose() {} }) },
};
class LanguageClient {
  constructor(id, name, server, options) { launched = { id, name, server, options }; }
  async start() {}
  async stop() {}
}
const original = Module._load;
Module._load = function (request, parent, isMain) {
  if (request === "vscode") return vscode;
  if (request === "vscode-languageclient/node") return { LanguageClient };
  return original.call(this, request, parent, isMain);
};
const extension = require(path.join(__dirname, "..", "extension.js"));
Module._load = original;

async function main() {
  extension.activate({ subscriptions: [] });
  assert.equal(launched.server.run.command, "bin/fango");
  assert.deepEqual(launched.server.run.args, ["lsp"]);
  assert.equal(launched.server.run.options.cwd, "/project");
  assert.equal(launched.options.documentSelector[0].language, "fango");
  assert.equal(launched.options.synchronize.fileEvents, watcher);
  await extension.deactivate();
}
main().catch(error => { console.error(error); process.exitCode = 1; });
