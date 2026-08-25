# Contributing

## Building

```
make build
```

Builds the `zen` binary with version + commit SHA stamped via ldflags. The installable package is `.flox/pkgs/zen.nix` (`flox build` → `./result-zen/bin/zen`, or `nix build`); it wraps `git` and `gh` so nix-darwin / the watch daemon do not need Flox on PATH. Or manually:

```
go build -o zen .
```

Verify your build:

```
zen version
```

## Testing

```
make test
```

## Architecture

See [docs/architecture.md](docs/architecture.md) for the daemon design, source-of-truth model, worktree naming conventions, and source tree layout.
