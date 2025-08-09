# recon

<p align="center">
  <img src="assets/recon-logo.svg" alt="recon logo" width="200" />
</p>

Track SQL executed by tests via a lightweight PostgreSQL wire-protocol proxy, and diff both queries and database schema against a baseline from your main branch.

## What this does
- Intercepts SQL by speaking the PostgreSQL wire protocol (captures both simple Query and extended Parse messages) and stores the query text.
- Exposes an HTTP API to retrieve captured queries and the current full schema.
- A CLI (this repo's `main.go`) fetches current queries and schema, fetches the latest baseline artifacts from the `main` branch, and produces JSON artifacts:
  - `sql-queries.json`: all queries captured this run
  - `queries-diff.json`: new queries not in baseline
  - `full-schema.json`: current schema (or baseline if skipping schema dump)
  - `schema-diff.json`: table-level schema differences vs baseline

## How it works
- A sidecar proxy (`sql-proxy`) listens on a port (default `5433`) and forwards to your real Postgres (`5432`).
- When your tests run against the proxy, it logs queries on Parse/Query and starts a background full-schema dump.
- The API exposes endpoints for queries and schema. The schema endpoint returns `503 Service Unavailable` while the dump is in progress and includes a `Retry-After` header.
- The CLI calls the API, fetches baseline artifacts from GitHub Actions, and writes JSON files you can upload as artifacts.

<p align="center">
  <img src="assets/how-it-works.svg" alt="How recon works: Tests/App -> sql-proxy -> Postgres; CLI reads Proxy API and baseline artifacts to produce diffs." width="820" />
</p>

## Requirements
- GitHub Actions or any CI runner that can run service containers
- Docker images:
  - Postgres (e.g. `postgres`)
  - Proxy: `ghcr.io/droptableifexists/sql-proxy:latest`
- For the CLI step (this repo): Go toolchain or a prebuilt binary. The CLI also fetches artifacts from the GitHub API using `GITHUB_TOKEN`.

## Configuration
### Proxy (`sql-proxy`) environment variables
- `LISTEN_PORT` (default: `5433`): Port the proxy listens on.
- `BACKEND_HOST` (default: `postgres`): Hostname of the real Postgres.
- `BACKEND_PORT` (default: `5432`): Port of the real Postgres.
- `PASS_THROUGH_PORT` (default: `5434`): Optional passthrough listener without interception.
- `API_PORT` (default: `8080`): Port for the proxy HTTP API.
- `DB_CONNECTION_STRING` (required for schema dump): Connection string for schema introspection, e.g. `host=postgres port=5432 user=postgres password=postgres sslmode=disable`.
- `DEFAULT_DATABASE` (optional, default: `postgres`): Database used to enumerate databases.

Notes:
- The proxy does not automatically start a schema dump. Trigger it by POSTing to `/schema_dump` after your migrations complete.

### CLI (diff tool in this repo) environment variables
- `SQL_PROXY_API_ADDRESS` (required): Address of the proxy API, e.g. `localhost:8080` (if ports are published) or `proxy:8080` (service name in Actions network).
- `TEST_SUITE_NAME` (optional, default: `default`): Namespaces artifacts so multiple test suites can maintain separate baselines.
- `GITHUB_REPOSITORY` (required for baseline fetch): `owner/repo`.
- `GITHUB_TOKEN` (required for baseline fetch): Token with `actions:read`.
- `SKIP_SCHEMA_DUMP` (optional): When set to `true`, skips schema API calls and uses the baseline schema from `main` directly. The schema diff will be an empty array (`[]`).

## GitHub Actions example
```yaml
name: CI

on:
  push:
    branches: [ main ]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres
        env:
          POSTGRES_PASSWORD: postgres
        ports:
          - 5432:5432
        options: >-
          --health-cmd pg_isready
          --health-interval 10s
          --health-timeout 5s
          --health-retries 5

      proxy:
        image: ghcr.io/droptableifexists/sql-proxy:latest
        env:
          LISTEN_PORT: 5433
          BACKEND_HOST: postgres
          BACKEND_PORT: 5432
          API_PORT: 8080
          DB_CONNECTION_STRING: host=postgres port=5432 user=postgres password=postgres sslmode=disable
          DEFAULT_DATABASE: postgres
        ports:
          - 5433:5433
          - 8080:8080

    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: stable

      - name: Run database migrations (example)
        env:
          DATABASE_URL: postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable
        run: ./migrate

      - name: Trigger schema dump
        run: |
          curl -sS -X POST http://localhost:8080/schema_dump

      - name: Run tests against proxy
        env:
          # Your app connects to the DB via the proxy on 5433
          DATABASE_URL: postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable
        run: |
          # Replace with your test command(s)
          go test ./...

      - name: Generate query and schema artifacts
        env:
          SQL_PROXY_API_ADDRESS: localhost:8080
          TEST_SUITE_NAME: default
          GITHUB_REPOSITORY: ${{ github.repository }}
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          # Optionally skip live schema and use baseline
          # SKIP_SCHEMA_DUMP: "true"
        run: |
          go run .

      - name: Upload SQL queries
        uses: actions/upload-artifact@v4
        with:
          name: sql-queries
          path: sql-queries.json

      - name: Upload queries diff
        uses: actions/upload-artifact@v4
        with:
          name: queries-diff
          path: queries-diff.json

      - name: Upload full schema
        uses: actions/upload-artifact@v4
        with:
          name: full-schema
          path: full-schema.json

      - name: Upload schema diff
        uses: actions/upload-artifact@v4
        with:
          name: schema-diff
          path: schema-diff.json
```

## Local usage (dev)
1. Start Postgres locally (Docker is fine) and export `DB_CONNECTION_STRING` for the proxy.
2. Run the proxy (ensure ports 5433 and 8080 are free):
   ```bash
   docker run --rm -p 5433:5433 -p 8080:8080 \
     -e LISTEN_PORT=5433 \
     -e BACKEND_HOST=host.docker.internal \
     -e BACKEND_PORT=5432 \
     -e API_PORT=8080 \
     -e DB_CONNECTION_STRING="host=host.docker.internal port=5432 user=postgres password=postgres sslmode=disable" \
     -e DEFAULT_DATABASE=postgres \
     ghcr.io/droptableifexists/sql-proxy:latest
   ```
3. Point your app/tests at `localhost:5433` and run them.
4. Generate artifacts:
   ```bash
   SQL_PROXY_API_ADDRESS=localhost:8080 \
   GITHUB_REPOSITORY=owner/repo \
   GITHUB_TOKEN=ghp_... \
   go run .
   ```
   To skip a live schema call and just use baseline:
   ```bash
   SKIP_SCHEMA_DUMP=true SQL_PROXY_API_ADDRESS=localhost:8080 go run .
   ```

## API endpoints (proxy)
- `GET /health`: Health check.
- `GET /queries`: Returns captured queries (JSON array of `{ "Query": "..." }`).
- `POST /schema_dump`: Triggers schema dump (the API also auto-starts it on boot).
- `GET /schema`:
  - `200 OK`: Full schema JSON when ready
  - `503 Service Unavailable`: Dump in progress, includes `Retry-After` header
  - `500 Internal Server Error`: Failed to fetch schema; response contains an `errors` array of messages

## Artifacts and baseline
- The CLI fetches artifacts filtered to `main` branch and the requested name, then picks the newest by `created_at`.
- Expected artifact names:
  - Queries: `sql-queries-<TEST_SUITE_NAME>`
  - Schema: `full-schema-<TEST_SUITE_NAME>`
- The CLI searches within the ZIP for `sql-queries.json` or `full-schema.json` and reads their contents.

## Troubleshooting
- Schema endpoint returns 503 and never completes
  - Ensure `DB_CONNECTION_STRING` is provided and reachable from the proxy container.
  - Check Postgres readiness/credentials.
- Schema endpoint returns 500 with `errors`
  - The response body includes a list of error messages. Common issues include bad connection strings or missing network access.
- Empty schema
  - Verify `DEFAULT_DATABASE` (falls back to `postgres`).
- No queries captured
  - Ensure your tests are hitting the proxy port (usually 5433) rather than connecting directly to Postgres.
- Baseline not found
  - Confirm `GITHUB_REPOSITORY` and `GITHUB_TOKEN` are set, and that artifacts exist on `main` with names containing the expected base names.

## Notes
- When `SKIP_SCHEMA_DUMP=true`, the CLI will:
  - Use the baseline schema from `main` as the current schema output
  - Produce an empty `schema-diff.json` (`[]`)
- Query diffing uses a hashmap for O(N+M) detection of new queries.

---

If you have feedback or feature requests, please open an issue or PR.