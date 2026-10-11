const assert = require("node:assert/strict");
const Module = require("node:module");
const path = require("node:path");

const launched = [];
const watchers = [];
const commands = new Map();
const logs = [];
let configured = "bin/fango";
let changedConfiguration;
let openedDocument;
let failStart = false;
let advertiseLibrary = true;
const document = {
  languageId: "fango",
  uri: { scheme: "file", fsPath: "/project/Main.fango" },
};
const folder = { uri: { fsPath: "/project" } };
const otherFolder = { uri: { fsPath: "/other" } };
// The resolved library can be outside the workspace, including a hidden
// installation directory. Only hidden paths *inside* it should be ignored.
const stdlibPath = "/install/.local/lib/fango/stdlib";
function disposable() { return { disposed: false, dispose() { this.disposed = true; } }; }
function makeWatcher(pattern) {
  const watcher = Object.assign(disposable(), { pattern, events: {} });
  for (const kind of ["Create", "Change", "Delete"]) {
    watcher[`onDid${kind}`] = handler => {
      watcher.events[kind] = handler;
      return disposable();
    };
  }
  watchers.push(watcher);
  return watcher;
}
const vscode = {
  Uri: { file: fsPath => ({ scheme: "file", fsPath }) },
  RelativePattern: class { constructor(base, pattern) { this.base = base; this.pattern = pattern; } },
  commands: { registerCommand: (name, handler) => { commands.set(name, handler); return disposable(); } },
  workspace: {
    textDocuments: [document, { ...document, uri: { ...document.uri, fsPath: "/project/Other.fango" } }],
    getWorkspaceFolder: uri => uri.fsPath.startsWith("/other/") ? otherFolder : folder,
    getConfiguration: () => ({ get: () => configured }),
    createFileSystemWatcher: makeWatcher,
    onDidOpenTextDocument: handler => { openedDocument = handler; return disposable(); },
    onDidChangeConfiguration: handler => { changedConfiguration = handler; return disposable(); },
  },
  window: { createOutputChannel: () => ({ appendLine: message => logs.push(message) }) },
  languages: { registerDocumentFormattingEditProvider: disposable },
};
let active = 0;
const activeRoots = new Set();
class LanguageClient {
  constructor(id, name, server, options) {
    Object.assign(this, { id, name, server, options, stopped: false });
    this.initializeResult = { capabilities: advertiseLibrary ? { experimental: { fango: { stdlibPath } } } : {} };
    launched.push(this);
  }
  async start() {
    if (failStart) throw new Error("startup failed");
    assert.ok(!activeRoots.has(this.server.run.options.cwd), "previous server must stop before its replacement starts");
    activeRoots.add(this.server.run.options.cwd);
    active++;
    this.started = true;
  }
  async stop() {
    // Yield so concurrent restarts would expose duplicate servers.
    await new Promise(resolve => setImmediate(resolve));
    if (this.started && !this.stopped) {
      active--;
      activeRoots.delete(this.server.run.options.cwd);
    }
    this.stopped = true;
  }
}
const original = Module._load;
Module._load = function (request, parent, isMain) {
  if (request === "vscode") return vscode;
  if (request === "vscode-languageclient/node") return { LanguageClient };
  return original.call(this, request, parent, isMain);
};
const extension = require(path.join(__dirname, "..", "extension.js"));
Module._load = original;

const tick = () => new Promise(resolve => setImmediate(resolve));
async function waitForLaunch(count) {
  const deadline = Date.now() + 3000;
  while (launched.length < count || !launched.at(-1).started) {
    assert.ok(Date.now() < deadline, `waiting for launch ${count}`);
    await new Promise(resolve => setTimeout(resolve, 10));
  }
  await tick();
}
function libraryWatcher() { return watchers.findLast(w => w.pattern.base.fsPath === stdlibPath); }
async function main() {
  const manifest = require("../package.json");
  assert.ok(manifest.contributes.commands.some(command => command.command === "fango.restartLanguageServer"));
  extension.activate({ subscriptions: [] });
  await waitForLaunch(1);
  assert.equal(launched.length, 1, "one server for two files in the same workspace");
  const first = launched[0];
  assert.equal(first.server.run.command, "bin/fango");
  assert.deepEqual(first.server.run.args, ["lsp"]);
  assert.equal(first.server.run.options.cwd, "/project");
  assert.equal(first.options.documentSelector[0].language, "fango");
  assert.equal(first.options.synchronize.fileEvents, watchers[0]);
  assert.equal(libraryWatcher().pattern.base.fsPath, stdlibPath);

  const restart = commands.get("fango.restartLanguageServer");
  assert.equal(typeof restart, "function");
  // Repeated manual requests and opens during restart must remain serialized.
  await Promise.all([restart(), restart(), openedDocument(document)]);
  assert.equal(launched.length, 3);
  assert.ok(launched.slice(0, -1).every(client => client.stopped));
  assert.ok(watchers.slice(0, -2).every(watcher => watcher.disposed));

  configured = "other/fango";
  changedConfiguration({ affectsConfiguration: name => name === "fango.path" });
  await waitForLaunch(4);
  assert.equal(launched.at(-1).server.run.command, configured);

  // A burst of source create/change/delete events causes one restart.
  const library = libraryWatcher();
  for (const kind of ["Create", "Change", "Delete"]) {
    library.events[kind](vscode.Uri.file(`${stdlibPath}/Http/Client.fango`));
  }
  await waitForLaunch(5);
  await new Promise(resolve => setTimeout(resolve, 350));
  assert.equal(launched.length, 5);
  assert.ok(library.disposed);

  for (const name of ["IO.native.go", "native_support.go"]) {
    libraryWatcher().events.Change(vscode.Uri.file(`${stdlibPath}/${name}`));
    await waitForLaunch(launched.length + 1);
  }
  const beforeIgnored = launched.length;
  libraryWatcher().events.Change(vscode.Uri.file(`${stdlibPath}/.fango/build/Main.fango`));
  libraryWatcher().events.Change(vscode.Uri.file(`${stdlibPath}/generated.go`));
  await new Promise(resolve => setTimeout(resolve, 350));
  assert.equal(launched.length, beforeIgnored);

  // A manual restart replaces every workspace's client, keeping one per root.
  const otherDocument = { ...document, uri: { ...document.uri, fsPath: "/other/Main.fango" } };
  vscode.workspace.textDocuments.push(otherDocument);
  const beforeOther = launched.length;
  openedDocument(otherDocument);
  await waitForLaunch(beforeOther + 1);
  assert.equal(active, 2);
  const oldClients = launched.slice();
  await restart();
  assert.equal(active, 2);
  assert.ok(oldClients.every(client => client.stopped));
  assert.equal(launched.length, beforeOther + 3);

  // Failed startup releases its watchers and permits a later manual retry.
  vscode.workspace.textDocuments = [document];
  failStart = true;
  await restart();
  assert.equal(active, 0);
  assert.ok(watchers.every(watcher => watcher.disposed));
  assert.ok(logs.some(message => message.includes("startup failed")));
  failStart = false;
  await restart();
  assert.equal(active, 1);

  // Older servers without the library capability still support manual restart.
  advertiseLibrary = false;
  await restart();
  assert.equal(active, 1);
  assert.ok(libraryWatcher().disposed);
  advertiseLibrary = true;
  await restart();

  // Shutdown cancels a pending automatic restart and cleans up all resources.
  libraryWatcher().events.Change(vscode.Uri.file(`${stdlibPath}/List.fango`));
  const beforeShutdown = launched.length;
  await extension.deactivate();
  await new Promise(resolve => setTimeout(resolve, 350));
  assert.equal(launched.length, beforeShutdown);
  assert.equal(active, 0);
  assert.ok(watchers.every(watcher => watcher.disposed));
}
main().catch(error => { console.error(error); process.exitCode = 1; });
