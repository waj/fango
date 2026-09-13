// A formatting provider for Fango, implemented by running `fango fmt -` over
// the buffer. The extension has no dependencies and no build step: `vscode` is
// supplied by the host at runtime and everything else is a Node builtin.

const vscode = require("vscode");
const { execFile } = require("child_process");
const fs = require("fs");
const path = require("path");

let output;

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

function activate(context) {
  context.subscriptions.push(
    vscode.languages.registerDocumentFormattingEditProvider("fango", {
      async provideDocumentFormattingEdits(document, _options, token) {
        const text = await formatted(document, token);
        if (text === null || text === document.getText()) {
          return [];
        }
        return [vscode.TextEdit.replace(wholeDocument(document), text)];
      },
    })
  );
}

function deactivate() {}

module.exports = { activate, deactivate };
