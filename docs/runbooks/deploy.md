# Deploy and operate Guidance

Place one instance per organization through landconfigregistry. Production public console: `https://guidance.<org>.mynimsforest.com`, fronted by Land HTTPS and application iamnim SSO. Internal bind: `127.0.0.1:8128`; never deploy with `--dev`.

Role environment: ORG_SLUG, LISTEN, BASE_URL, IAMNIM_URL, NATS_URL, DATA_DIR, MYCELIUM_PROXY_URL. LEDGER_URL and ODOO_URL are optional operator-configured read endpoints; UI cannot select arbitrary URLs. They default to organization-local ports 8087 and 8095. If the org bus requires credentials, mount its scoped NATS_CREDENTIALS through the organization's existing credential mechanism; do not copy tokens into roles.

Persistent host directory `/opt/nimsforestguidance/data`, owned by UID 10001, mounted at `/data`. SQLite uses WAL and synchronous FULL. Organization binding is stored in the database; reusing a data volume for another organization is refused. `signing.key` is a private local command/provenance signing key; retain it with the data and never commit it.

Any current organization member can create drafts and submit. Current organization administrators can review, approve or return submitted/reviewed drafts. The application validates iamnim identity and token-scoped membership on every request, so removed memberships or revoked sessions do not survive in a local cache. The new domain uses iamnim's existing trusted mynimsforest.com callback rule.

## Backup and restore

```sh
ssh root@<land> 'docker exec nimsforestguidance nimsforestguidance backup --output /data/backup-YYYYMMDD.db'
```

Backup uses SQLite VACUUM INTO for a consistent copy; an existing output file is refused. Copy the resulting database and signing.key to the organization's approved off-host backup location without exposing the key. Only operational administrators should access these files. The backup contains forecast history, approvals, operation outcomes and outbox rows. Exports are useful for reporting, but a full restore uses the database and signing key.

Restore: stop this container, retain the existing database and WAL/SHM files as a rollback set, restore the backup database and signing.key under correct ownership, remove only the stopped database's old WAL/SHM files after retaining them, and replant. Verify `/health`, organization binding, approved versions and history. Restore is exercised in the automated store tests.

## Source setup

Manual and CSV entry need no upstream setup. Optional connected source previews require a working related service and its `api_token` provisioned in service identity, authorized by the Land's identity binding. The app obtains it at use time through loopback myceliumproxy. Ledger category forecasts do not assert cash timing/currency; Odoo invoices are not guaranteed receipts and require expected cash dates. Preview, reconcile, and approve a version. A source read failure leaves manual drafts/history intact.

## Deployment and rollback

GitHub Actions tests/vets, builds an amd64 binary, obtains a short-lived GitHub OIDC registry token and publishes tagged images. Register repo `nimsforestguidance` with service allowlist `["nimsforestguidance"]` on mycelium. Update the role to a reviewed image digest, preserving previous role/image for rollback. Avoid reseeding every role to add one service; update the current role with this additive block and retain all other fields. Land pulls/reconciles and creates the domain route. Do not restart unrelated organization services.

Health checks storage, the durable guidance consumer and identity configuration. Tool heartbeat only means presence; source preview status and each row's observation timestamps describe data availability. No source integration is required for healthy manual operation.
