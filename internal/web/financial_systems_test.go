package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nimsforest/nimsforestguidance/internal/guidance"
)

func TestFinancialSystemsSeparateDesignationSetupAndAvailability(t *testing.T) {
	credentialStatus := 403
	sourceStatus := 200
	integrationBody := "[]"
	catalogOrg := "test"
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tools":
			json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "org_slug": catalogOrg, "tools": []any{map[string]any{"key": "odoo", "name": "Odoo", "kind": "service", "owner": "odoo", "source": "announced", "status": "online"}}})
		case "/api/org/integrations":
			w.Write([]byte(integrationBody))
		case "/api/identity/credentials/nimsforestledger", "/api/identity/credentials/nimsforestodoo":
			w.WriteHeader(credentialStatus)
			w.Write([]byte(`{"secrets":{"api_token":"private-test-token"}}`))
		case "/api/v1/ledgers", "/api/v1/companies":
			w.WriteHeader(sourceStatus)
			w.Write([]byte(`[{"id":"private-company","name":"private-resource-name"}]`))
		case "/health":
			w.WriteHeader(503)
			w.Write([]byte(`{"tool":"nimsforestokioki","status":"degraded","checks":{"credentials":"missing","bus":"ok"}}`))
		}
	}))
	defer fixture.Close()
	for _, key := range []string{"TOOLS_REGISTRY_URL", "MYCELIUM_PROXY_URL", "LEDGER_URL", "ODOO_URL", "OKIOKI_URL"} {
		t.Setenv(key, fixture.URL)
	}
	s := &Server{Store: &guidance.Store{Org: "test"}}
	read := func() map[string]any {
		w := httptest.NewRecorder()
		s.financialSystems(w, httptest.NewRequest("GET", "/api/financial-systems?organization=other", nil))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		if strings.Contains(w.Body.String(), "private-") {
			t.Fatal("private source metadata escaped status projection")
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	provider := func(out map[string]any, key string) map[string]any {
		for _, v := range out["providers"].([]any) {
			p := v.(map[string]any)
			if p["key"] == key {
				return p
			}
		}
		t.Fatal("missing provider")
		return nil
	}
	out := read()
	if out["organization"] != "test" {
		t.Fatal("browser organization overrode tenant")
	}
	if provider(out, "odoo")["availability"] != "online" || provider(out, "odoo")["configuration"] != "Setup required" {
		t.Fatal("runtime heartbeat invented configured credentials")
	}
	for _, v := range out["systems"].([]any) {
		if v.(map[string]any)["status"] != "Not set" {
			t.Fatal("available provider invented authoritative designation")
		}
	}
	credentialStatus = 200
	out = read()
	if provider(out, "odoo")["configuration"] != "Configured" {
		t.Fatal("successful resource read not recognized")
	}
	if out["systems"].([]any)[1].(map[string]any)["status"] != "Not set" {
		t.Fatal("connected Odoo silently became accounting authority")
	}
	integrationBody = `[{"type":"financial_systems","data":{"schema_version":1,"organization":"test","accounting":{"provider_key":"odoo","resource_ref":"company-17"}}}]`
	out = read()
	if out["systems"].([]any)[1].(map[string]any)["status"] != "Set" {
		t.Fatal("explicit organization designation missing")
	}
	sourceStatus = 500
	out = read()
	if provider(out, "odoo")["configuration"] != "Unavailable" {
		t.Fatal("source outage reported as missing setup")
	}
	integrationBody = `[{"type":"financial_systems","data":{"schema_version":1,"organization":"other","accounting":{"provider_key":"odoo"}}}]`
	catalogOrg = "other"
	out = read()
	if out["designation_status"] != "Unavailable" || out["catalog_available"] != false {
		t.Fatal("cross-organization projection accepted")
	}
}

func TestFinancialBindingsRejectAmbiguousAndRevokedMetadata(t *testing.T) {
	data := json.RawMessage(`{"schema_version":1,"organization":"test","accounting":{"provider_key":"odoo"}}`)
	if _, e := readFinancialBindings([]financialIntegration{{Type: "financial_systems", Data: data}, {Type: "financial_systems", Data: data}}, true, "test"); e == nil {
		t.Fatal("ambiguous designation accepted")
	}
	if _, e := readFinancialBindings(nil, false, "test"); e == nil {
		t.Fatal("configuration outage treated as absence")
	}
	if recordedConnection(json.RawMessage(`{"revoked_at":"2026-09-17"}`)) != "Revoked" {
		t.Fatal("revoked grant reported as usable")
	}
}
