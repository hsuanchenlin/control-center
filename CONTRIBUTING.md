# Contributing

## Continuous integration

Every pull request and every push to `main` runs the CI workflow in
[`.github/workflows/ci.yml`](.github/workflows/ci.yml). It uses the Go version
declared there (currently `1.26.3`) and runs with read-only repository
permissions.

The workflow enforces, on every run:

- `gofmt` formatting of all tracked `*.go` files
- `go vet ./...`
- `go test -race ./...`

Superseded runs on the same ref are cancelled automatically.
