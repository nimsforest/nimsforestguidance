package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nimsforest/nimsforestguidance/internal/guidance"
)

type portfolioTransport func(*http.Request) (*http.Response, error)

func (f portfolioTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPortfolioScopesReadsAndRejectsWrongTenant(t *testing.T) {
	t.Setenv("GUIDANCE_WORKSPACES", `{"home":"Home","allowed":"Allowed","wrong":"Wrong","denied":"Denied","outsider":"Outsider"}`)
	store, err := guidance.Open(t.TempDir()+"/state.db", "home")
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	s := &Server{Store: store, Auth: NewAuthMiddleware(AuthConfig{})}
	prior := portfolioClient
	defer func() { portfolioClient = prior }()
	var mu sync.Mutex
	hosts := []string{}
	portfolioClient = &http.Client{Transport: portfolioTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		hosts = append(hosts, r.URL.Host)
		mu.Unlock()
		c, e := r.Cookie("guidance_session")
		if e != nil || c.Value != "test-session" {
			t.Error("user session not forwarded")
		}
		if r.URL.Path != "/api/forecasts" || r.URL.Query().Get("horizon") != "13w" {
			t.Error("incorrect read target")
		}
		status := 200
		body := `[{"forecast":{"org":"allowed","id":"f1","entity_ref":"same"},"view":{"known":false}}]`
		if strings.Contains(r.URL.Host, "wrong") {
			body = `[{"forecast":{"org":"outsider"}}]`
		}
		if strings.Contains(r.URL.Host, "denied") {
			status = 403
			body = `{}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	u := &SessionUser{Memberships: []Membership{{Slug: "home"}, {Slug: "allowed"}, {Slug: "wrong"}, {Slug: "denied"}, {Slug: "not-deployed"}}}
	r := httptest.NewRequest("GET", "/api/portfolio?horizon=13w&organization=outsider&url=http://evil", nil)
	r.AddCookie(&http.Cookie{Name: "guidance_session", Value: "test-session"})
	r = r.WithContext(context.WithValue(r.Context(), authUserKey{}, u))
	w := httptest.NewRecorder()
	s.portfolio(w, r)
	var result struct {
		Forecasts   []portfolioEntry `json:"forecasts"`
		Unavailable []string         `json:"unavailable"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Forecasts) != 1 || result.Forecasts[0].Forecast.Org != "allowed" {
		t.Fatal(w.Body.String())
	}
	if strings.Join(result.Unavailable, ",") != "denied,wrong" {
		t.Fatal(result.Unavailable)
	}
	if len(hosts) != 3 {
		t.Fatal(hosts)
	}
	for _, h := range hosts {
		if strings.Contains(h, "outsider") || strings.Contains(h, "not-deployed") || strings.Contains(h, "evil") {
			t.Fatal(h)
		}
	}
}
