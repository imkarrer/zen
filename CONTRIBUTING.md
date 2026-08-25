# Contributing

## Building

```
make build
```

Builds the `zen` binary with version + commit SHA stamped via ldflags. With the Flox environment activated, `flox build` produces the same binary at `./result-zen/bin/zen` from `.flox/pkgs/zen.nix`. Or manually:

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
