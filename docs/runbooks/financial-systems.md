# Organization financial systems in Guidance

Members can see **Systems & inputs** and `/api/financial-systems` for the current organization. This is a read projection: Guidance owns forecasts, not provider configuration or record-system designation. Connectors remain managed through organization Admin. Source preview/import still requires organization administrator rights. Status responses never include service credentials, company/ledger resource payloads or integration configuration bodies.

Read sources are the same as Admin: the shared schema-1 tools catalog at fixed loopback `TOOLS_REGISTRY_URL` (default port8196) and the organization-attested `/api/org/integrations` through `MYCELIUM_PROXY_URL` (default8190). Browser parameters cannot select another organization or an upstream URL. Catalog schema and org identity are validated. Released/announced support and observed runtime are separate from organization setup. Tools assigned to accounting/cashflow responsibilities are discovered dynamically; Ledger, Odoo, Exact Online and OkiOki remain visible when missing from the catalog so missing setup is understandable.

Ledger and Odoo setup are verified with organization-scoped server-side source reads, returning status only. Missing/inaccessible source credentials show Setup required. Transient failures show Unavailable. OkiOki uses its port8107 health checks and distinguishes missing credentials from failing session/collection checks. Exact Online (and newly discovered providers) show recorded organization connection metadata separately from verified live access; Guidance does not yet implement their forecast previews.

## Designation contract for the shared setup owner

The tools/connector work does not yet publish explicit financial system-of-record designations. **Do not infer a designation from an installed tool, heartbeat, released assignment or successful import.** Until the shared owner records a designation, Guidance displays Not set while still reporting source setup. Configuration outages or mismatched orgs display Unavailable rather than Not set.

Guidance's read adapter supports this proposed optional organization integration metadata. The shared owner must agree/publish the contract and provide its authorized setup UI; this change does not add a separate Guidance setter or publish guessed values:

```json
{"type":"financial_systems","data":{"schema_version":1,"organization":"<attested-org>","cashflow":{"provider_key":"ledger","resource_ref":"<optional-resource>"},"accounting":{"provider_key":"odoo","resource_ref":"<optional-company>"}}}
```

Only one schema-1 record matching the attested organization is accepted; empty roles mean Not set. Duplicate, malformed or mismatched metadata yields Unavailable. Provider keys are stable identifiers, not executable commands or redirect URLs. Bindings remain visible when their provider becomes unavailable; connection loss never silently changes the organization's designated system. If the shared owner chooses a different representation, update only this adapter.

Verification covers runtime-without-credentials, configured-without-designation, explicit designation, source outages, revoked grants, ambiguous/mismatched org metadata and private-payload exclusion. Refresh status to re-read the underlying services. These checks do not start collection or silently change forecasts.
