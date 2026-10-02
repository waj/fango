{
  description = "Fango — a statically compiled functional language on the Go runtime";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-darwin" "x86_64-darwin" "aarch64-linux" "x86_64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: {
        default = self.packages.${pkgs.stdenv.hostPlatform.system}.fango;

        fango = pkgs.buildGoModule {
          pname = "fango";
          version = "0-unstable-${self.shortRev or self.dirtyShortRev or "dev"}";
          src = self;
          vendorHash = "sha256-rjwyCO3fsA4rgX7/zDqZWsQJ+l2SHqcQrfgO8TJOJXM=";
          subPackages = [ "cmd/fango" ];

          # The correctness suite is `make test` in the development shell; it
          # opens TCP listeners and takes minutes, so it does not gate a build.
          doCheck = false;

          nativeBuildInputs = [ pkgs.gnumake pkgs.makeWrapper ];
          allowGoReference = true;

          # The compiler finds its library at ../lib/fango beside the
          # executable (doc/reference/commands.md, "The library root"), and
          # both compiled programs and the interpreter's native worker are
          # built with `go build`. Wrap it with the Go it was built with, so
          # the toolchain matches the generated go.mod on any machine.
          postInstall = ''
            make install-lib PREFIX=$out
            wrapProgram $out/bin/fango --prefix PATH : ${pkgs.go}/bin
          '';

          meta = {
            description = "A statically compiled functional language on the Go runtime";
            homepage = "https://github.com/waj/fango";
            mainProgram = "fango";
          };
        };
      });

      apps = forAllSystems (pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${pkgs.stdenv.hostPlatform.system}.fango}/bin/fango";
          meta.description = "The Fango compiler, interpreter, REPL, and language server";
        };
      });

      templates.default = {
        path = ./templates/default;
        description = "A Fango project with the compiler in its development shell";
      };

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go       # compiler implementation language and codegen backend
            gopls    # LSP for editors
            gotools  # goimports etc.
            gnumake  # Makefile convenience targets
            nodejs   # runs the TextMate grammar check
            python3  # repository scripts and ad hoc development tools
          ];

          # The grammar check tokenizes with vscode-textmate. Build its two
          # pure-JS packages from editors/vscode/package-lock.json and put
          # them on NODE_PATH, so the check needs no npm install and leaves no
          # node_modules in the checkout.
          env.NODE_PATH = "${pkgs.importNpmLock.buildNodeModules {
            npmRoot = ./editors/vscode;
            inherit (pkgs) nodejs;
          }}/node_modules";

          # Run .githooks/pre-commit, which rejects build outputs, on commits
          # from this checkout. `make ci` runs the same check.
          shellHook = ''
            if git rev-parse --git-dir >/dev/null 2>&1; then
              git config core.hooksPath .githooks
            fi
          '';
        };

        # Opt-in tools for the whole-document JSON comparison. Keep GHC and
        # Aeson out of the ordinary compiler development shell.
        jsoncompare = pkgs.mkShell {
          inputsFrom = [ self.devShells.${pkgs.stdenv.hostPlatform.system}.default ];
          packages = [ (pkgs.haskellPackages.ghcWithPackages (p: [ p.aeson ])) ];
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixpkgs-fmt);
    };
}
