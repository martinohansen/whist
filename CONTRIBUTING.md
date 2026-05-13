# Contributing

Pull requests are welcome. For major changes, please open an issue first
to discuss what you would like to change.

Please make sure to update tests as appropriate.

## Quick start

```bash
git clone https://github.com/martinohansen/whist.git
cd whist
make dev
```

Open `http://localhost:8080` and you're in. The `make dev` target
rebuilds on file changes; `whist.db` (SQLite) is created in the working
directory on first run.

### Layout

A single binary serves the app. The package root holds the HTTP layer:
one central route table dispatches each URL to a handler, and each
handler loads data through the store and renders a template. Reusable
pieces — the SQLite-backed store, domain types, scoring rules — live
under `internal/`. Templates and static assets are embedded at build
time and served from their own top-level directories. To find the code
behind any URL, start at the route table.

## Development

Clone the repo and run the server directly:

```bash
git clone https://github.com/martinohansen/whist.git
cd whist
go run .
```

For a live-reloading loop, use the `make dev` target (requires
[entr](https://eradman.com/entrproject/)):

```bash
make dev
```

Run the tests before submitting:

```bash
go test ./...
```
