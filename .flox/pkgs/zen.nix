{
  buildGoModule,
  lib,
  makeWrapper,
  git,
  gh,
}:

buildGoModule {
  pname = "zen";
  version = "dev";

  src = ../../.;

  # Keep in sync with go.sum. `flox build` / `nix build` prints the correct
  # hash when this is empty or wrong.
  vendorHash = "sha256-A1JmmPLT0ys5we8CXu4ade+dGuJLG4rTIKW2rOmQ5wo=";

  env.CGO_ENABLED = "0";

  ldflags = [
    "-s"
    "-w"
    "-X github.com/mgreau/zen/cmd.Version=dev"
    "-X github.com/mgreau/zen/cmd.Commit=unknown"
  ];

  nativeBuildInputs = [ makeWrapper ];

  # zen execs git and gh; wrap them so the binary works without a project env.
  postInstall = ''
    wrapProgram $out/bin/zen --prefix PATH : ${lib.makeBinPath [ git gh ]}
  '';

  meta = {
    description = "Worktree orchestrator for AI-assisted PR reviews and feature work";
    homepage = "https://github.com/mgreau/zen";
    license = lib.licenses.mit;
    mainProgram = "zen";
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
}
