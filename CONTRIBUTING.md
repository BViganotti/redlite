# Contributing

Thanks for taking a look. Redlite is a small project with a small surface, so
the rules are short.

## Development setup

You need Go 1.24 or newer. Docker is optional, for the image target.

```console
git clone https://github.com/BViganotti/redlite
cd redlite
make test
make race
```

Useful targets:

| Target | What it does |
| --- | --- |
| `make build` | Build `bin/redlite` |
| `make test` | Unit and integration tests |
| `make race` | Tests with the race detector |
| `make cover` | Coverage profile and per-function summary |
| `make vet` | `go vet ./...` |
| `make fmt` | `gofmt -w` |
| `make docker` | Build the container image |

## Before you open a pull request

- `make fmtcheck && make vet && make race` all pass.
- New behaviour comes with a test. For protocol changes, add a case to
  `internal/resp/resp_test.go`; for commands, add an integration test that speaks
  the wire protocol.
- Do not change the append-only file format without a migration story. Existing
  logs must still load.
- Keep replies byte-compatible with Redis. If you deliberately diverge, document
  it in `docs/compatibility.md`.

## Commit messages

Conventional Commits, please:

```
feat(strings): add SETRANGE
fix(aof): truncate a partial trailing command
docs: clarify SCAN cursor semantics
test(store): cover expiry races
```

## Reporting a bug

Include the redlite version (`./bin/redlite --version`), the command you sent,
the reply you got, and the reply you expected from Redis. A minimal reproduction
is worth more than a paragraph.

Security issues: please do not open a public issue. Email the maintainer
directly; there is no bug bounty, but there will be credit.

## Scope

Redlite is deliberately small. Features that make it a different product —
Lua scripting, cluster mode, modules, TLS termination — are out of scope. The
roadmap in `ROADMAP.md` is the place to argue about what comes next.
