package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	tool "github.com/nimsforest/nimsforesttool"
)

type financialProvider struct {
	Key           string `json:"key"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	Availability  string `json:"availability"`
	Configuration string `json:"configuration"`
	Detail        string `json:"detail"`
	ImportSupport string `json:"import_support"`
}
type financialBinding struct {
	Provider string `json:"provider_key"`
	Resource string `json:"resource_ref,omitempty"`
}
type financialDesignations struct {
	Schema     int              `json:"schema_version"`
	Org        string           `json:"organization"`
	Cashflow   financialBinding `json:"cashflow"`
	Accounting financialBinding `json:"accounting"`
}
type financialIntegration struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

var financialClient = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func financialRead(ctx context.Context, base, path string, dst any) (int, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+path, nil)
	if e != nil {
		return 0, e
	}
	resp, e := financialClient.Do(req)
	if e != nil {
		return 0, e
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if e != nil || len(body) > 2<<20 {
		return resp.StatusCode, errors.New("unsupported status response")
	}
	if resp.StatusCode != 200 && resp.StatusCode != 503 {
		return resp.StatusCode, errors.New("status service unavailable")
	}
	return resp.StatusCode, json.Unmarshal(body, dst)
}
func (s *Server) financialSystems(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	providers := []financialProvider{
		{Key: "ledger", Name: "Ledger", Role: "Cashflow records", Configuration: "Unavailable", ImportSupport: "Preview supported"},
		{Key: "odoo", Name: "Odoo", Role: "Accounting records", Configuration: "Unavailable", ImportSupport: "Preview supported"},
		{Key: "exact_online", Name: "Exact Online", Role: "Accounting records", Configuration: "Unavailable", ImportSupport: "Guidance preview not yet supported"},
		{Key: "okioki", Name: "OkiOki", Role: "Supporting bank and accounting-document connector", Configuration: "Unavailable", ImportSupport: "Guidance preview not yet supported"},
	}
	var catalog tool.Catalog
	var integrations []financialIntegration
	var catalogOK, integrationsOK bool
	var wait sync.WaitGroup
	wait.Add(5)
	go func() {
		defer wait.Done()
		status, e := financialRead(ctx, setting("TOOLS_REGISTRY_URL", "http://127.0.0.1:8196"), "/api/tools", &catalog)
		catalogOK = e == nil && status == 200 && catalog.SchemaVersion == tool.CatalogSchemaVersion && catalog.OrgSlug == s.Store.Org
		if catalogOK {
			for _, entry := range catalog.Tools {
				if tool.ValidateDefinitions([]tool.Definition{entry.Definition}) != nil || entry.Owner == "" || (entry.Source != "announced" && entry.Source != "released") {
					catalogOK = false
					break
				}
			}
		}
	}()
	go func() {
		defer wait.Done()
		status, e := financialRead(ctx, setting("MYCELIUM_PROXY_URL", "http://127.0.0.1:8190"), "/api/org/integrations", &integrations)
		integrationsOK = e == nil && status == 200
	}()
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wait.Done()
			path := "/api/v1/ledgers"
			if i == 1 {
				path = "/api/v1/companies"
			}
			var resources []json.RawMessage
			if e := s.sourceGET(ctx, providers[i].Key, path, &resources); e == nil {
				if len(resources) > 0 {
					providers[i].Configuration = "Configured"
					providers[i].Detail = "Organization source read succeeded. Confirm the resource and cash dates before importing."
				} else {
					providers[i].Configuration = "Connected, no resources"
					providers[i].Detail = "The source is reachable but has no organization resources configured."
				}
			} else {
				var credential sourceCredentialError
				if errors.As(e, &credential) && (credential.Status == 403 || credential.Status == 404) {
					providers[i].Configuration = "Setup required"
					providers[i].Detail = "A source credential is missing or is not accessible to Guidance; upstream setup has not been verified."
				} else {
					providers[i].Detail = "Source setup could not be verified. Check this organization's connector settings."
				}
			}
		}(i)
	}
	go func() {
		defer wait.Done()
		var health struct {
			Checks map[string]string `json:"checks"`
			Status string            `json:"status"`
			Tool   string            `json:"tool"`
		}
		status, e := financialRead(ctx, setting("OKIOKI_URL", "http://127.0.0.1:8107"), "/health", &health)
		if e != nil || health.Tool != "nimsforestokioki" {
			return
		}
		if c, ok := health.Checks["credentials"]; ok {
			if c != "ok" {
				providers[3].Configuration = "Not set"
				providers[3].Detail = "OkiOki reports missing credentials."
			} else if status == 200 && health.Status == "ok" {
				providers[3].Configuration = "Configured"
				providers[3].Detail = "Credentials, session and collection health checks passed."
			} else {
				providers[3].Configuration = "Needs attention"
				providers[3].Detail = "Credentials are present but a session or collection check failed."
			}
		}
	}()
	wait.Wait()
	keys := map[string]string{"ledger": "ledger", "nimsforestledger": "ledger", "odoo": "odoo", "nimsforestodoo": "odoo", "exact_online": "exact_online", "exactonline": "exact_online", "nimsforestexactonline": "exact_online", "okioki": "okioki", "nimsforestokioki": "okioki"}
	if catalogOK {
		for _, entry := range catalog.Tools {
			if _, known := keys[entry.Key]; known {
				continue
			}
			role := ""
			for _, assignment := range entry.Assignments {
				if assignment.Responsibility == "accounting" {
					role = "Accounting records"
				} else if assignment.Responsibility == "cashflow" {
					role = "Cashflow records"
				}
			}
			if role != "" {
				keys[entry.Key] = entry.Key
				if entry.Connection != nil {
					keys[entry.Connection.IntegrationType] = entry.Key
				}
				providers = append(providers, financialProvider{Key: entry.Key, Name: entry.Name, Role: role, Configuration: "Unavailable", Detail: "Use organization connector setup to verify this source.", ImportSupport: "Guidance preview not yet supported"})
			}
		}
	}
	for i := range providers {
		providers[i].Availability = "Unavailable"
		if catalogOK {
			providers[i].Availability = "Not listed"
			for _, entry := range catalog.Tools {
				if keys[entry.Key] == providers[i].Key {
					providers[i].Availability = entry.Status
					if providers[i].Availability == "" {
						providers[i].Availability = "Listed"
					}
				}
			}
		}
		if integrationsOK && providers[i].Configuration == "Unavailable" && providers[i].Key != "ledger" && providers[i].Key != "odoo" && providers[i].Key != "okioki" {
			providers[i].Configuration = "Not set"
			providers[i].Detail = "No organization connection is recorded and source setup has not been verified."
			for _, integration := range integrations {
				if keys[integration.Type] == providers[i].Key {
					providers[i].Configuration = recordedConnection(integration.Data)
					providers[i].Detail = "Organization connection metadata; live source access has not been verified."
				}
			}
		}
	}
	bindings, err := readFinancialBindings(integrations, integrationsOK, s.Store.Org)
	state := "Available"
	if err != nil {
		state = "Unavailable"
	}
	roles := []map[string]any{}
	for _, role := range []string{"cashflow", "accounting"} {
		binding := bindings.Cashflow
		if role == "accounting" {
			binding = bindings.Accounting
		}
		status := "Not set"
		if err != nil {
			status = "Unavailable"
		}
		name := ""
		configuration := ""
		if binding.Provider != "" && err == nil {
			status = "Set"
			name = binding.Provider
			for _, p := range providers {
				if p.Key == binding.Provider || keys[binding.Provider] == p.Key {
					name = p.Name
					configuration = p.Configuration
				}
			}
		}
		roles = append(roles, map[string]any{"key": role, "status": status, "provider_key": binding.Provider, "name": name, "resource_ref": binding.Resource, "configuration": configuration})
	}
	respond(w, 200, map[string]any{"organization": s.Store.Org, "checked_at": time.Now().UTC().Format(time.RFC3339), "designation_status": state, "catalog_available": catalogOK, "connections_available": integrationsOK, "systems": roles, "providers": providers, "manage_url": "https://admin." + s.Store.Org + ".mynimsforest.com/connectors"})
}

var financialProviderKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

func readFinancialBindings(integrations []financialIntegration, available bool, org string) (financialDesignations, error) {
	var out financialDesignations
	if !available {
		return out, errors.New("organization configuration unavailable")
	}
	found := false
	for _, integration := range integrations {
		if integration.Type != "financial_systems" {
			continue
		}
		if found {
			return financialDesignations{}, errors.New("ambiguous organization configuration")
		}
		found = true
		if json.Unmarshal(integration.Data, &out) != nil || out.Schema != 1 || out.Org != org {
			return financialDesignations{}, errors.New("organization configuration mismatch")
		}
		if out.Cashflow.Provider != "" && !financialProviderKey.MatchString(out.Cashflow.Provider) {
			return financialDesignations{}, errors.New("unsupported cashflow designation")
		}
		if out.Accounting.Provider != "" && !financialProviderKey.MatchString(out.Accounting.Provider) {
			return financialDesignations{}, errors.New("unsupported accounting designation")
		}
	}
	return out, nil
}
func recordedConnection(raw json.RawMessage) string {
	var metadata struct {
		Revoked string `json:"revoked_at"`
	}
	if json.Unmarshal(raw, &metadata) != nil {
		return "Unavailable"
	}
	if metadata.Revoked != "" {
		return "Revoked"
	}
	return "Connection recorded"
}
