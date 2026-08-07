# Contributing

## Requirements

- Go version specified by `go.mod`
- Apple Silicon macOS 14 or later for Lume integration work
- Lume 0.5.1 for VM integration tests

## Build

```sh
git clone https://github.com/markwylde/gitea-runner-lume.git
cd gitea-runner-lume
go build -o ./gitea-runner-lume .
./gitea-runner-lume version
```

## Test

```sh
go test ./internal/...
go test -race ./internal/pkg/lume ./internal/pkg/guestagent ./internal/app/run ./internal/app/cmd
go vet ./...
```

Read the governing feature in `specs/features/` before changing behavior. Keep
active implementation checklists in `specs/tasks/`.
