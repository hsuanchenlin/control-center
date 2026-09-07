# Contributing

## Continuous integration

Every pull request and every push to `main` runs the CI workflow in
[`.github/workflows/ci.yml`](.github/workflows/ci.yml). It uses the Go version
declared there (currently `1.26.3`) and runs with read-only repository
permissions.

Once the repository contains a `go.mod`, the workflow enforces, on every run:

- `gofmt` formatting of all tracked `*.go` files
- `go vet ./...`
- `go test -race ./...`

Until the Go module lands, those checks report that they are inactive and the
workflow still passes. They require no configuration changes to activate: they
switch on automatically as soon as `go.mod` exists.

Superseded runs on the same ref are cancelled automatically.
