# Contributing

Contributions are welcome.

Before submitting changes, run:

```bash
go mod tidy
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
```

Please keep changes small and focused.

When sharing a source snapshot, create it from git instead of archiving the
working directory. This avoids including `.git/`, `node_modules/`, coverage
profiles, and other generated files:

```bash
git archive --format=tar.gz --output=paping-go.tar.gz HEAD
git archive --format=zip --output=paping-go.zip HEAD
```
