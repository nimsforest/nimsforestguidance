package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSSORequiresCurrentOrganizationMembership(t *testing.T) {
	member := true
	revoked := false
	id := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie("iamnim_session")
		if e != nil || c.Value != "valid" || revoked {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/me" {
			w.Write([]byte(`{"user_id":"coo","name":"COO"}`))
		} else if member {
			w.Write([]byte(`[{"organization_slug":"test","organization_name":"Test Organization","is_admin":true},{"organization_slug":"executxr","organization_name":"ExecutXR"},{"organization_slug":"thenaturalbeautyclub","organization_name":"The Natural Beauty Club"}]`))
		} else {
			w.Write([]byte(`[{"organization_slug":"other","is_admin":true}]`))
		}
	}))
	defer id.Close()
	a := NewAuthMiddleware(AuthConfig{IamNimURL: id.URL, BaseURL: "https://guidance.test.mynimsforest.com", OrgSlug: "test"})
	h := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := a.GetUser(r)
		if len(u.Memberships) != 3 || u.Memberships[1].Name != "ExecutXR" {
			t.Error("live organization choices missing")
		}
		if !u.IsAdmin {
			t.Error("admin role missing")
		}
		w.WriteHeader(200)
	}))
	req := func(path string, cookie bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if cookie {
			r.AddCookie(&http.Cookie{Name: "guidance_session", Value: "valid"})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := req("/", false); w.Code != 303 || w.Header().Get("Location") == "" {
		t.Fatal("unauthenticated request did not go to SSO")
	}
	if w := req("/?token=valid", false); w.Code != 303 || len(w.Result().Cookies()) == 0 {
		t.Fatal("SSO callback failed")
	}
	if w := req("/new?token=valid", false); w.Code != 303 || w.Header().Get("Location") != "/new" {
		t.Fatal("new forecast intent lost at SSO callback")
	}
	if req("/api/forecasts", true).Code != 200 {
		t.Fatal("member denied")
	}
	member = false
	if req("/api/forecasts", true).Code != 403 {
		t.Fatal("cross-org membership accepted")
	}
	member = true
	revoked = true
	if req("/api/forecasts", true).Code == 200 {
		t.Fatal("revoked session accepted")
	}
}
func TestCSRFRequiresExactOrigin(t *testing.T) {
	s := &Server{Base: "https://guidance.test.mynimsforest.com"}
	r := httptest.NewRequest("POST", "/api/commands", nil)
	r.AddCookie(&http.Cookie{Name: "guidance_csrf", Value: "01234567890123456789012345678901"})
	r.Header.Set("X-CSRF-Token", "01234567890123456789012345678901")
	r.Header.Set("Origin", "https://evil.test")
	if s.csrf(r) {
		t.Fatal("cross-origin write accepted")
	}
	r.Header.Set("Origin", s.Base)
	if !s.csrf(r) {
		t.Fatal("valid origin rejected")
	}
	r.Header.Set("X-CSRF-Token", "other")
	if s.csrf(r) {
		t.Fatal("bad CSRF token accepted")
	}
}
