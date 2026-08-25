{
  description = "zen — worktree orchestrator for AI-assisted PR reviews";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "aarch64-darwin"
        "x86_64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
      gitCommit = self.shortRev or self.dirtyShortRev or "dirty";
      version = self.shortRev or self.dirtyShortRev or "dev";
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          buildGoModule =
            pkgs.buildGo125Module or (pkgs.buildGoModule.override { go = pkgs.go_1_25; });
          zen = (pkgs.callPackage ./.flox/pkgs/zen.nix { inherit buildGoModule; }).overrideAttrs {
            inherit version;
            src = self;
            ldflags = [
              "-s"
              "-w"
              "-X github.com/mgreau/zen/cmd.Version=${version}"
              "-X github.com/mgreau/zen/cmd.Commit=${gitCommit}"
            ];
          };
        in
        {
          inherit zen;
          default = zen;
        }
      );

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/zen";
        };
      });
    };
}
