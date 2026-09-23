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
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixpkgs-fmt);
    };
}
