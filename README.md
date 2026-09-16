# nimsforestguidance

Versioned management cash forecasts and a calculated funding outlook. Users can enter every input manually, import CSV, or preview authorized inputs from related Ledger/Odoo tools and fill the gaps manually.

Guidance records approved expectations and their evidence. Actual transactions stay in accounting tools. Funding need is the maximum cumulative liquidity shortfall, with restricted cash and minimum cash buffers accounted for. Financing counts only when marked committed with supporting evidence, on its expected usable date. Conditional/proposed or unevidenced financing is excluded.

The first release supports legal entity, brand, operator and location references; base/downside/upside scenarios; dated cash schedules; assumptions; an administrator review/approval workflow; immutable approved versions; audit history; cash curves; weekly/monthly/quarterly/annual outlook horizons; CSV and full JSON export; and source previews with signed provenance. Portfolio totals show gross entity needs by currency, not freely transferable consolidated cash.

Source imports are optional and reviewed. Ledger seeds monthly category forecasts with month-end placeholder cash dates and no currency declaration. Odoo seeds outstanding posted invoice/bill residuals per selected company; its current connector does not expose expected cash dates. Users must supply those dates and adjustment reasons. Source reads require organization administrator membership and a configured service identity API credential via myceliumproxy. No credential is entered into this application. Importing the same source record twice is refused; refreshing a source displays changes for manual review rather than silently replacing saved inputs.

Master: https://issues.nimsforest.mynimsforest.com/issues/412
Architecture: https://github.com/nimsforest/nimsforest2/tree/main/docs/architecture

## Run locally

```sh
go build -o bin/nimsforestguidance ./cmd/nimsforestguidance
ORG_SLUG=local ./bin/nimsforestguidance serve --dev --addr 127.0.0.1:8128 --data ./data
```

Development mode has no authentication and is only for local use. Production requires an organization bus with existing TAPROOT/HUMUS/RIVER streams, iamnim SSO, persistent storage and a land role. Without a bus the service reports degraded and refuses writes. Existing connections reconnect automatically; a bus absent at initial startup requires a service restart after the bus is ready.

```sh
go test ./...
CGO_ENABLED=1 go test -race ./...
go vet ./...
```

CI includes the shared nimsforesttool black-box contract and durable-bus, approval, timing, source provenance and backup/restore checks.

## Interfaces

Browser commands are authenticated, checked for same-origin/CSRF, signed locally, then queued durably on `tap.guidance.forecast.<action>`. Actions: `save`, `submit`, `review`, `approve`, `return`, `revise`. The Rootlet stores idempotent command results and revisions atomically in SQLite. A durable outbox emits command outcomes to Humus and changed-record observations through River. Only signed console commands carry named-person approval authority; unsigned organization-bus commands cannot grant themselves administrator rights.

Organization-local reads use `guidance.query` with `{"org":"<slug>"}`; the `list` CLI exposes this. Registrations declare stable tool key `guidance`, exact shipped CLI commands, and Numbers/Nurture/Napoleon assignments. Discovery is not an access grant; catalog assignments remain unresolved if a NIM is absent.

See [operations](docs/runbooks/deploy.md) for placement, access and backups. This release does not perform FX consolidation, automatic transfer elimination, investor CRM, or autonomous source polling. Cross-entity transfers must be modeled explicitly; portfolio totals remain gross needs.
