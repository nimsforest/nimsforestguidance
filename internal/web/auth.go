package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AuthConfig struct {
	IamNimURL string
	BaseURL   string
	OrgSlug   string
}

type SessionUser struct {
	UserID  string `json:"user_id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"-"`
}

type authUserKey struct{}

type AuthMiddleware struct{ config AuthConfig }

func NewAuthMiddleware(config AuthConfig) *AuthMiddleware { return &AuthMiddleware{config: config} }

// Wrap checks identity and current, token-scoped membership on every request.
// A cached identity must never outlive membership or token revocation.
func (a *AuthMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/api/v1/health" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if a.config.OrgSlug == "" || a.config.IamNimURL == "" || a.config.BaseURL == "" {
			http.Error(w, "organization authorization is not configured", http.StatusServiceUnavailable)
			return
		}
		token := r.URL.Query().Get("token")
		callback := token != ""
		if !callback {
			if c, err := r.Cookie("guidance_session"); err == nil {
				token = c.Value
			}
		}
		if token == "" {
			a.redirectToLogin(w, r)
			return
		}
		user, err := a.validateToken(r.Context(), token)
		if err != nil {
			status := http.StatusServiceUnavailable
			var denied authError
			if errors.As(err, &denied) {
				status = int(denied)
			}
			if status == http.StatusUnauthorized {
				http.SetCookie(w, &http.Cookie{Name: "guidance_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
				if !callback && r.Method == http.MethodGet {
					a.redirectToLogin(w, r)
					return
				}
			}
			message := "organization access denied"
			if status == http.StatusServiceUnavailable {
				message = "identity service unavailable; retry shortly"
			}
			http.Error(w, message, status)
			return
		}
		if callback {
			http.SetCookie(w, &http.Cookie{Name: "guidance_session", Value: token, Path: "/", MaxAge: 86400 * 30, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
			http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authUserKey{}, user)))
	})
}

func (a *AuthMiddleware) GetUser(r *http.Request) *SessionUser {
	u, _ := r.Context().Value(authUserKey{}).(*SessionUser)
	return u
}

func (a *AuthMiddleware) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	// A form post must not become the post-login return target: iamnim sends
	// the browser back with a GET, and a POST-only path then answers 404
	// (#276). Return to the same-origin page that carried the form instead.
	target := r.URL.Path
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		target = "/"
		if ref, err := url.Parse(r.Referer()); err == nil && ref.Path != "" && (ref.Host == "" || strings.TrimSuffix(a.config.BaseURL, "/") == ref.Scheme+"://"+ref.Host) {
			target = ref.Path
		}
	}
	http.Redirect(w, r, a.config.IamNimURL+"/login?redirect_uri="+url.QueryEscape(a.config.BaseURL+target), http.StatusSeeOther)
}

var identityClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type authError int

func (e authError) Error() string { return http.StatusText(int(e)) }

func (a *AuthMiddleware) validateToken(ctx context.Context, token string) (*SessionUser, error) {
	fetch := func(path string, dst any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.config.IamNimURL+path, nil)
		if err != nil {
			return err
		}
		req.AddCookie(&http.Cookie{Name: "iamnim_session", Value: token})
		resp, err := identityClient.Do(req)
		if err != nil {
			return fmt.Errorf("identity service unavailable")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				return authError(resp.StatusCode)
			}
			return fmt.Errorf("identity service status %d", resp.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dst)
	}
	var user SessionUser
	if err := fetch("/api/me", &user); err != nil {
		return nil, err
	}
	if user.UserID == "" {
		return nil, fmt.Errorf("missing identity")
	}
	var memberships []struct {
		Slug    string `json:"organization_slug"`
		IsAdmin bool   `json:"is_admin"`
	}
	if err := fetch("/api/me/memberships", &memberships); err != nil {
		return nil, err
	}
	for _, m := range memberships {
		if m.Slug == a.config.OrgSlug {
			user.IsAdmin = m.IsAdmin
			return &user, nil
		}
	}
	return nil, authError(http.StatusForbidden)
}
