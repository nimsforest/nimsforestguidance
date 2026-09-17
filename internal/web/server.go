package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nimsforest/nimsforestguidance/internal/guidance"
	tool "github.com/nimsforest/nimsforesttool"
)

//go:embed static/*
var assets embed.FS

type Server struct {
	Store *guidance.Store
	Auth  *AuthMiddleware
	NC    *nats.Conn
	JS    nats.JetStreamContext
	Base  string
	Dev   bool
	Key   []byte
}

func SigningKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, "signing.key")
	b, e := os.ReadFile(path)
	if e == nil {
		if len(b) != 64 {
			return nil, errors.New("invalid signing key")
		}
		return b, nil
	}
	if !os.IsNotExist(e) {
		return nil, e
	}
	b = []byte(guidance.NewID() + guidance.NewID())
	e = os.WriteFile(path, b, 0600)
	return b, e
}
func (s *Server) commandMAC(c guidance.Command) string {
	c.Signature = ""
	b, _ := json.Marshal(c)
	h := hmac.New(sha256.New, s.Key)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Server) originMAC(m guidance.Movement) string {
	b, _ := json.Marshal(struct {
		Source, Ref, ImportedAt, SourceDate string
		Original                            *guidance.SourceValue
	}{m.Source, m.SourceRef, m.ImportedAt, m.SourceDate, m.Original})
	h := hmac.New(sha256.New, s.Key)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Server) validateOrigins(f guidance.Forecast) error {
	for _, m := range f.Movements {
		if m.Source != "" && m.Source != "manual" && m.Source != "csv" {
			if m.Original == nil || !hmac.Equal([]byte(m.OriginSignature), []byte(s.originMAC(m))) {
				return errors.New("imported provenance was changed; preview the source again")
			}
		}
	}
	return nil
}

// Deployment metadata describes availability, never membership or approval rights.
func (s *Server) workspaces(u *SessionUser) map[string]string {
	var deployed map[string]string
	json.Unmarshal([]byte(os.Getenv("GUIDANCE_WORKSPACES")), &deployed)
	out := map[string]string{}
	for _, m := range u.Memberships {
		if name, ok := deployed[m.Slug]; ok {
			out[m.Slug] = name
		}
	}
	if _, ok := out[s.Store.Org]; !ok {
		out[s.Store.Org] = s.Store.Org
	}
	return out
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	health := tool.HealthHandler("nimsforestguidance", map[string]tool.Check{
		"storage": func() error { return s.Store.DB.Ping() },
		"bus": func() error {
			if s.Dev {
				return nil
			}
			if s.NC == nil || !s.NC.IsConnected() || s.JS == nil {
				return errors.New("organization bus unavailable")
			}
			if _, e := s.JS.ConsumerInfo("TAPROOT", "guidance-writes"); e != nil {
				return errors.New("durable command consumer unavailable")
			}
			return nil
		},
		"identity": func() error {
			if s.Dev {
				return nil
			}
			if s.Auth == nil || s.Auth.config.IamNimURL == "" {
				return errors.New("iamnim is not configured")
			}
			return nil
		},
	})
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /api/v1/health", health)
	files, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(files))))
	page := func(w http.ResponseWriter, r *http.Request) {
		b, _ := assets.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	}
	mux.HandleFunc("GET /{$}", page)
	mux.HandleFunc("GET /new", page)
	mux.HandleFunc("GET /api/financial-systems", s.financialSystems)
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		u := s.user(r)
		csrf := ""
		if c, e := r.Cookie("guidance_csrf"); e == nil {
			csrf = c.Value
		}
		if csrf == "" {
			csrf = guidance.NewID()
			http.SetCookie(w, &http.Cookie{Name: "guidance_csrf", Value: csrf, Path: "/", Secure: !s.Dev, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
		}
		respond(w, 200, map[string]any{"user": u, "is_admin": u.IsAdmin, "org": s.Store.Org, "csrf": csrf, "version": "0.2.1", "workspaces": s.workspaces(u)})
	})
	mux.HandleFunc("GET /api/forecasts", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.List()
		if e != nil {
			fail(w, 500, "forecast storage unavailable")
			return
		}
		out := []any{}
		for _, f := range v {
			end := r.URL.Query().Get("end")
			start, _ := time.Parse("2006-01-02", f.AsOf)
			switch r.URL.Query().Get("horizon") {
			case "13w":
				end = start.AddDate(0, 0, 91).Format("2006-01-02")
			case "12m":
				end = start.AddDate(0, 12, 0).Format("2006-01-02")
			case "3y":
				end = start.AddDate(3, 0, 0).Format("2006-01-02")
			}
			out = append(out, map[string]any{"forecast": f, "view": guidance.Calculate(f, end)})
		}
		respond(w, 200, out)
	})
	mux.HandleFunc("GET /api/forecasts/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.History(r.PathValue("id"))
		if e != nil {
			fail(w, 500, "history unavailable")
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("POST /api/commands", s.command)
	mux.HandleFunc("GET /api/operations/{ref}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.Result(r.PathValue("ref"))
		if e != nil {
			respond(w, 200, map[string]string{"status": "queued", "ref": r.PathValue("ref")})
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("GET /api/sources", s.sources)
	mux.HandleFunc("GET /api/sources/{source}/preview", s.preview)
	mux.HandleFunc("POST /api/csv/preview", s.csvPreview)
	mux.HandleFunc("GET /api/forecasts/{id}/csv", s.csvExport)
	mux.HandleFunc("GET /api/templates/cash-schedule.csv", s.csvTemplate)
	mux.HandleFunc("GET /api/export", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.List()
		if e != nil {
			fail(w, 500, "export unavailable")
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="guidance-snapshot.json"`)
		respond(w, 200, map[string]any{"org": s.Store.Org, "exported_at": time.Now().UTC(), "model": guidance.ModelVersion, "forecasts": v})
	})
	var handler http.Handler = mux
	if !s.Dev {
		handler = s.Auth.Wrap(handler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if r.Method != "GET" && r.Method != "HEAD" {
			if !s.csrf(r) {
				fail(w, 403, "request origin or form token invalid; refresh this page")
				return
			}
		}
		handler.ServeHTTP(w, r)
	})
}
func (s *Server) user(r *http.Request) *SessionUser {
	if s.Dev {
		return &SessionUser{UserID: "local-developer", Name: "Local developer", IsAdmin: true}
	}
	return s.Auth.GetUser(r)
}
func (s *Server) csrf(r *http.Request) bool {
	if s.Dev {
		return true
	}
	c, e := r.Cookie("guidance_csrf")
	if e != nil || len(c.Value) != 32 || !hmac.Equal([]byte(c.Value), []byte(r.Header.Get("X-CSRF-Token"))) {
		return false
	}
	origin := r.Header.Get("Origin")
	u, e := url.Parse(s.Base)
	return e == nil && origin == u.Scheme+"://"+u.Host
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	respond(w, status, map[string]string{"error": msg})
}
func (s *Server) command(w http.ResponseWriter, r *http.Request) {
	var c guidance.Command
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		fail(w, 400, "invalid command")
		return
	}
	u := s.user(r)
	if u == nil {
		fail(w, 401, "sign in required")
		return
	}
	c.Org = s.Store.Org
	c.Actor = u.UserID
	c.CanApprove = u.IsAdmin
	c.Signature = ""
	if c.Ref == "" {
		c.Ref = guidance.NewID()
	}
	if c.Action == "save" {
		if e := s.validateOrigins(c.Forecast); e != nil {
			fail(w, 400, e.Error())
			return
		}
		if e := guidance.Validate(c.Forecast, false); e != nil {
			fail(w, 400, e.Error())
			return
		}
	}
	if s.Dev {
		v, e := s.Store.Apply(c)
		if e != nil {
			fail(w, 500, "could not save command")
			return
		}
		respond(w, 200, v)
		return
	}
	if s.JS == nil || s.NC == nil || !s.NC.IsConnected() {
		fail(w, 503, "durable command queue unavailable; your changes have not been saved")
		return
	}
	c.Signature = s.commandMAC(c)
	b, _ := json.Marshal(c)
	if _, e := s.JS.Publish("tap.guidance.forecast."+c.Action, b, nats.MsgId("guidance-"+c.Ref)); e != nil {
		fail(w, 503, "could not queue changes; retry with the same operation reference")
		return
	}
	respond(w, 202, map[string]string{"status": "queued", "ref": c.Ref})
}

func (s *Server) StartBus(ctx context.Context) error {
	if s.NC == nil {
		return errors.New("no bus connection")
	}
	js, e := s.NC.JetStream()
	if e != nil {
		return e
	}
	s.JS = js
	sub, e := js.PullSubscribe("tap.guidance.forecast.>", "guidance-writes", nats.BindStream("TAPROOT"), nats.ManualAck(), nats.AckExplicit(), nats.AckWait(30*time.Second))
	if e != nil {
		return e
	}
	go func() {
		for ctx.Err() == nil {
			msgs, e := sub.Fetch(10, nats.MaxWait(time.Second))
			if e != nil {
				if e != nats.ErrTimeout {
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Second):
					}
				}
				continue
			}
			for _, m := range msgs {
				var c guidance.Command
				b := m.Data
				var leaf struct {
					Data   json.RawMessage `json:"data"`
					Source string          `json:"source"`
				}
				json.Unmarshal(b, &leaf)
				if len(leaf.Data) > 0 {
					b = leaf.Data
				}
				if json.Unmarshal(b, &c) != nil || c.Org != s.Store.Org || c.Ref == "" {
					m.Term()
					continue
				}
				// Only the authenticated local console can sign named-person approval rights.
				if !hmac.Equal([]byte(c.Signature), []byte(s.commandMAC(c))) {
					c.CanApprove = false
					c.Actor = "bus:" + leaf.Source
					if leaf.Source == "" {
						c.Actor = "bus:guidance-client"
					}
				}
				if c.Action != strings.TrimPrefix(m.Subject, "tap.guidance.forecast.") {
					m.Term()
					continue
				}
				if c.Action == "save" && s.validateOrigins(c.Forecast) != nil {
					m.Term()
					continue
				}
				if _, e := s.Store.Apply(c); e != nil {
					m.NakWithDelay(time.Second)
					continue
				}
				m.Ack()
			}
		}
	}()
	_, e = s.NC.Subscribe("guidance.query", func(m *nats.Msg) {
		var q struct {
			Org string `json:"org"`
		}
		if json.Unmarshal(m.Data, &q) != nil || q.Org != s.Store.Org {
			m.Respond([]byte(`{"error":"organization mismatch"}`))
			return
		}
		v, e := s.Store.List()
		if e != nil {
			m.Respond([]byte(`{"error":"storage unavailable"}`))
			return
		}
		b, _ := json.Marshal(v)
		m.Respond(b)
	})
	if e != nil {
		return e
	}
	go s.flushOutbox(ctx)
	return s.NC.Flush()
}
func (s *Server) flushOutbox(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		rows, e := s.Store.DB.Query(`SELECT ref,data,humus_sent,river_sent FROM outbox WHERE humus_sent=0 OR river_sent=0 LIMIT 50`)
		if e != nil {
			continue
		}
		type item struct {
			ref, data    string
			humus, river int
		}
		items := []item{}
		for rows.Next() {
			var i item
			rows.Scan(&i.ref, &i.data, &i.humus, &i.river)
			items = append(items, i)
		}
		rows.Close()
		for _, i := range items {
			if i.humus == 0 {
				if _, e := s.JS.Publish("humus.guidance.command.result", []byte(i.data), nats.MsgId("guidance-humus-"+i.ref)); e == nil {
					s.Store.DB.Exec(`UPDATE outbox SET humus_sent=1 WHERE ref=?`, i.ref)
				}
			}
			if i.river == 0 {
				b, _ := json.Marshal(map[string]any{"subject": "river.guidance.forecast.changed", "data": json.RawMessage(i.data), "source": "nimsforestguidance", "ts": time.Now().UTC()})
				if _, e := s.JS.Publish("river.guidance.forecast.changed", b, nats.MsgId("guidance-river-"+i.ref)); e == nil {
					s.Store.DB.Exec(`UPDATE outbox SET river_sent=1 WHERE ref=?`, i.ref)
				}
			}
		}
	}
}
