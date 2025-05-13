# recon
Github action for tracking queries executed by tests via postrges wire protocol

## Usage

```yaml
    services:
      postgres:
        image: postgres
        env:
          POSTGRES_PASSWORD: postgres
        options: >-
          --health-cmd pg_isready
          --health-interval 10s
          --health-timeout 5s
          --health-retries 5
        ports:
          - 5432:5432

      proxy:
        image: ghcr.io/droptableifexists/sql-proxy:latest
        env:
          LISTEN_PORT: 5433
          BACKEND_HOST: postgres
          BACKEND_PORT: 5432
          API_PORT: 8080
        ports:
          - 5433:5433
          - 8080:8080
    steps:
      - name: Checkout
        id: checkout
        uses: actions/checkout@v4
      - name: Run Tests
        run: |
          cd tests
          go run .
      - name: Get SQL data
        uses: droptableifexists/reconnaissance@main
        id: get-sql-data
      - name: Save queries to file
        run: |
          cat <<EOF > queries.json
          ${{ steps.get-sql-data.outputs.sql-queries }}
          EOF
      - name: Upload SQL Queries Artifact
        uses: actions/upload-artifact@v4
        with:
          name: sql-queries
          path: queries.json
```

## Output
Inside the `steps.get-sql-data.outputs.sql-queries` the folloing json object is set
```json
[{"Query":"SELECT 1 as one;"},{"Query":"SELECT 2 as two;"}]
```
## Comming Soon
1. Ability to see changes between commits
2. Full DB Schema
3. Ability to also see changes from full BD schema