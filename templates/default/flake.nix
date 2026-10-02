{
  description = "A Fango project";

  inputs = {
    fango.url = "github:waj/fango";
    nixpkgs.follows = "fango/nixpkgs";
  };

  outputs = { self, nixpkgs, fango }:
    let
      systems = [ "aarch64-darwin" "x86_64-darwin" "aarch64-linux" "x86_64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system nixpkgs.legacyPackages.${system});
    in
    {
      devShells = forAllSystems (system: pkgs: {
        default = pkgs.mkShell {
          packages = [ fango.packages.${system}.default ];
        };
      });
    };
}
