package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nimsforest/nimsforestguidance/internal/guidance"
)

func TestDurableWritesApprovalAuthorityAndOutcomes(t *testing.T) {
	ns, e := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	go ns.Start()
	defer ns.Shutdown()
	if !ns.ReadyForConnections(5 * time.Second) {
		t.Fatal("bus did not start")
	}
	nc, e := nats.Connect(ns.ClientURL())
	if e != nil {
		t.Fatal(e)
	}
	defer nc.Close()
	js, _ := nc.JetStream()
	for name, subject := range map[string]string{"TAPROOT": "tap.>", "HUMUS": "humus.>", "RIVER": "river.>"} {
		if _, e = js.AddStream(&nats.StreamConfig{Name: name, Subjects: []string{subject}, Storage: nats.FileStorage}); e != nil {
			t.Fatal(e)
		}
	}
	store, e := guidance.Open(filepath.Join(t.TempDir(), "guidance.db"), "test")
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	s := &Server{Store: store, NC: nc, Key: []byte("test-key")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if e = s.StartBus(ctx); e != nil {
		t.Fatal(e)
	}
	wait := func(ref string) guidance.Result {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			r, e := store.Result(ref)
			if e == nil {
				return r
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("command was not delivered")
		return guidance.Result{}
	}
	send := func(c guidance.Command, sign bool) guidance.Result {
		if sign {
			c.Signature = s.commandMAC(c)
		}
		b, _ := json.Marshal(c)
		if _, e = js.Publish("tap.guidance.forecast."+c.Action, b); e != nil {
			t.Fatal(e)
		}
		return wait(c.Ref)
	}
	n := int64(10000)
	f := guidance.Forecast{Entity: "Operator", EntityRef: "op", Owner: "COO", Scenario: "base", AsOf: "2026-09-16", Horizon: "2027-09-16", Currency: "EUR", Opening: &n, OpeningStatus: "verified", OpeningEvidence: "Statement", Complete: true, Assumptions: "Cash dates reviewed", Movements: []guidance.Movement{}}
	c := guidance.Command{Ref: "create", Org: "test", Action: "save", Forecast: f, Actor: "coo", CanApprove: true}
	r := send(c, true)
	if r.Status != "applied" {
		t.Fatalf("save: %+v", r)
	}
	if send(c, true).ID != r.ID {
		t.Fatal("redelivery duplicated forecast")
	}
	c = guidance.Command{Ref: "submit", Org: "test", Action: "submit", Forecast: guidance.Forecast{ID: r.ID}, Expected: 1, Actor: "coo", CanApprove: true}
	if send(c, true).Status != "applied" {
		t.Fatal("submit failed")
	}
	c.Ref = "forged-review"
	c.Action = "review"
	c.Expected = 2
	c.Signature = "forged"
	if send(c, false).Status != "failed" {
		t.Fatal("bus client forged administrator rights")
	}
	c.Ref = "review"
	if send(c, true).Status != "applied" {
		t.Fatal("signed review failed")
	}
	c.Ref = "approve"
	c.Action = "approve"
	c.Expected = 3
	if send(c, true).Status != "applied" {
		t.Fatal("signed approval failed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var pending int
		store.DB.QueryRow(`SELECT count(*) FROM outbox WHERE humus_sent=0 OR river_sent=0`).Scan(&pending)
		if pending == 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, name := range []string{"HUMUS", "RIVER"} {
		info, e := js.StreamInfo(name)
		if e != nil || info.State.Msgs == 0 {
			t.Fatalf("%s has no durable outcome", name)
		}
	}
	b, _ := json.Marshal(map[string]string{"org": "other"})
	reply, e := nc.Request("guidance.query", b, time.Second)
	if e != nil || string(reply.Data) != `{"error":"organization mismatch"}` {
		t.Fatal("cross-org query not rejected")
	}
}

func TestLedgerPreviewKeepsSignedProvenance(t *testing.T) {
	credentials := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"secrets":{"api_token":"test-source-token"}}`))
	}))
	defer credentials.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-source-token" {
			t.Error("source credential missing")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"category":"rent","month":"2026-09","amount":-150000}]`))
	}))
	defer source.Close()
	t.Setenv("MYCELIUM_PROXY_URL", credentials.URL)
	t.Setenv("LEDGER_URL", source.URL)
	s := &Server{Dev: true, Key: []byte("test-key")}
	r := httptest.NewRequest("GET", "/api/sources/ledger/preview?resource=ledger-1&currency=EUR", nil)
	r.SetPathValue("source", "ledger")
	w := httptest.NewRecorder()
	s.preview(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var p struct {
		Movements []guidance.Movement `json:"movements"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	if len(p.Movements) != 1 || p.Movements[0].Date != "2026-09-30" || p.Movements[0].Kind != "payment" {
		t.Fatal("ledger mapping wrong")
	}
	f := guidance.Forecast{Movements: p.Movements}
	if e := s.validateOrigins(f); e != nil {
		t.Fatal(e)
	}
	f.Movements[0].Original.Amount = 1
	if s.validateOrigins(f) == nil {
		t.Fatal("imported source metadata was forgeable")
	}
}
