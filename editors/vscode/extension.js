// Fango's formatter runs over the buffer; its language server supplies
// navigation, hover, and diagnostics for open files.

const vscode = require("vscode");
const { execFile } = require("child_process");
const fs = require("fs");
const path = require("path");
const { LanguageClient } = require("vscode-languageclient/node");

let output;
const clients = new Map();
const watchers = new Map();
let lifecycle = Promise.resolve();
let restartTimer;
let disposed = false;

// Serialize startup and restart so repeated commands or file events cannot
// leave two servers running for the same workspace.
function enqueue(task) {
  lifecycle = lifecycle.then(() => disposed ? undefined : task()).catch(error => {
    log(`Fango language server: ${error}`);
  });
  return lifecycle;
}

function scheduleRestart(uri, stdlibPath) {
  // Build/cache directories beneath a library are not library sources.
  if (path.relative(stdlibPath, uri.fsPath).split(path.sep).some(part => part.startsWith("."))) return;
  if (!uri.fsPath.endsWith(".fango") && !uri.fsPath.endsWith(".native.go") &&
      path.basename(uri.fsPath) !== "native_support.go") return;
  clearTimeout(restartTimer);
  restartTimer = setTimeout(() => { void enqueue(restartClients); }, 250);
}

function disposeWatchers() {
  for (const resources of watchers.values()) {
    for (const resource of resources) resource.dispose();
  }
  watchers.clear();
}

function log(message) {
  if (!output) {
    output = vscode.window.createOutputChannel("Fango");
  }
  output.appendLine(message);
}

// binaryFor resolves the compiler to run. An explicit `fango.path` wins;
// otherwise a `fango` built at the workspace root is preferred over one on
// PATH, so working on the compiler formats with the compiler you just built.
function binaryFor(document) {
  const configured = vscode.workspace
    .getConfiguration("fango", document.uri)
    .get("path");
  if (configured) {
    return configured;
  }
  const folder = vscode.workspace.getWorkspaceFolder(document.uri);
  if (folder) {
    const local = path.join(folder.uri.fsPath, "fango");
    if (fs.existsSync(local)) {
      return local;
    }
  }
  return "fango";
}

// cwdFor runs the formatter beside the file, so a relative `fango.path` and
// any future project lookup resolve the way the user would expect.
function cwdFor(document) {
  const folder = vscode.workspace.getWorkspaceFolder(document.uri);
  if (folder) {
    return folder.uri.fsPath;
  }
  return document.isUntitled ? undefined : path.dirname(document.uri.fsPath);
}

// formatted resolves to the formatted source, or to null when the formatter
// declined. Declining is normal — a buffer mid-edit often does not parse, and
// `fango fmt` leaves such a file alone rather than guessing — so the reason
// goes to the output channel and the save proceeds unchanged.
function formatted(document, token) {
  return new Promise((resolve) => {
    const binary = binaryFor(document);
    const child = execFile(
      binary,
      ["fmt", "-"],
      { cwd: cwdFor(document), maxBuffer: 64 * 1024 * 1024 },
      (err, stdout, stderr) => {
        if (!err) {
          resolve(stdout);
          return;
        }
        if (err.code === "ENOENT") {
          log(
            `Could not run ${binary}. Build it with 'make build', or set ` +
              `"fango.path" to the fango executable.`
          );
        } else if (stderr) {
          log(stderr.trimEnd());
        } else {
          log(String(err.message || err));
        }
        resolve(null);
      }
    );

    if (token) {
      token.onCancellationRequested(() => child.kill());
    }
    child.stdin.on("error", () => {});
    child.stdin.end(document.getText());
  });
}

function wholeDocument(document) {
  return new vscode.Range(
    document.positionAt(0),
    document.positionAt(document.getText().length)
  );
}

function clientKey(document) {
  const folder = vscode.workspace.getWorkspaceFolder(document.uri);
  return folder ? folder.uri.fsPath : path.dirname(document.uri.fsPath);
}

async function startClient(document) {
  if (document.languageId !== "fango" || document.uri.scheme !== "file") return;
  const key = clientKey(document);
  if (clients.has(key)) return;
  const folder = vscode.workspace.getWorkspaceFolder(document.uri);
  const selector = folder
    ? [{ scheme: "file", language: "fango", pattern: new vscode.RelativePattern(folder, "**/*.fango") }]
    : [{ scheme: "file", language: "fango" }];
  const options = { cwd: cwdFor(document) };
  const executable = binaryFor(document);
  const watcher = folder
    ? vscode.workspace.createFileSystemWatcher(new vscode.RelativePattern(folder, "**/*.fango"))
    : vscode.workspace.createFileSystemWatcher("**/*.fango");
  const resources = [watcher];
  watchers.set(key, resources);
  const client = new LanguageClient(
    "fango",
    "Fango",
    { run: { command: executable, args: ["lsp"], options }, debug: { command: executable, args: ["lsp"], options } },
    { documentSelector: selector, synchronize: { fileEvents: watcher }, outputChannel: output || (output = vscode.window.createOutputChannel("Fango")) }
  );
  clients.set(key, client);
  try {
    await client.start();
    const stdlibPath = client.initializeResult?.capabilities.experimental?.fango?.stdlibPath;
    if (typeof stdlibPath === "string" && path.isAbsolute(stdlibPath)) {
      // Bundled sources are a process-lifetime snapshot. A saved change needs
      // a new process, even when ordinary watched-file notifications recheck.
      const libraryWatcher = vscode.workspace.createFileSystemWatcher(
        new vscode.RelativePattern(vscode.Uri.file(stdlibPath), "**/*.{fango,go}")
      );
      const changed = uri => scheduleRestart(uri, stdlibPath);
      resources.push(
        libraryWatcher,
        libraryWatcher.onDidCreate(changed),
        libraryWatcher.onDidChange(changed),
        libraryWatcher.onDidDelete(changed)
      );
    }
  } catch (error) {
    clients.delete(key);
    watchers.delete(key);
    for (const resource of resources) resource.dispose();
    log(`Could not start Fango language server (${executable} lsp): ${error}`);
    await client.stop();
  }
}

async function restartClients() {
  clearTimeout(restartTimer);
  const running = [...clients.values()];
  clients.clear();
  disposeWatchers();
  await Promise.all(running.map(client => client.stop()));
  for (const document of vscode.workspace.textDocuments) {
    await startClient(document);
  }
}

function activate(context) {
  disposed = false;
  context.subscriptions.push(
    vscode.languages.registerDocumentFormattingEditProvider("fango", {
      async provideDocumentFormattingEdits(document, _options, token) {
        const text = await formatted(document, token);
        if (text === null || text === document.getText()) {
          return [];
        }
        return [vscode.TextEdit.replace(wholeDocument(document), text)];
      },
    }),
    vscode.commands.registerCommand("fango.restartLanguageServer", () => enqueue(restartClients)),
    vscode.workspace.onDidOpenTextDocument(document => { void enqueue(() => startClient(document)); }),
    vscode.workspace.onDidChangeConfiguration(event => {
      if (event.affectsConfiguration("fango.path")) void enqueue(restartClients);
    })
  );
  for (const document of vscode.workspace.textDocuments) {
    void enqueue(() => startClient(document));
  }
}

async function deactivate() {
  disposed = true;
  clearTimeout(restartTimer);
  await lifecycle;
  const running = [...clients.values()];
  clients.clear();
  disposeWatchers();
  await Promise.all(running.map(client => client.stop()));
}

module.exports = { activate, deactivate };
