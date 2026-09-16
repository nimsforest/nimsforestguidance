package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nimsforest/nimsforestguidance/internal/guidance"
	"github.com/nimsforest/nimsforestguidance/internal/web"
	tool "github.com/nimsforest/nimsforesttool"
)

var version = "v0.1.0"

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func main() {
	if e := run(); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}
func run() error {
	command := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	if command == "version" {
		fmt.Println(version)
		return nil
	}
	if command == "help" || command == "--help" {
		fmt.Println("nimsforestguidance serve [--addr ADDRESS] [--data DIRECTORY] [--dev]\nnimsforestguidance list\nnimsforestguidance backup --data DIRECTORY --output FILE\nnimsforestguidance version")
		return nil
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	addr := flags.String("addr", "", "listen address")
	data := flags.String("data", env("DATA_DIR", "./data"), "persistent data directory")
	dev := flags.Bool("dev", false, "local development only; no authentication or bus")
	output := flags.String("output", "", "backup destination")
	if e := flags.Parse(args); e != nil {
		return e
	}
	org, e := tool.RequireOrg(os.Getenv("ORG_SLUG"))
	if e != nil {
		return e
	}
	if command == "list" {
		options := []nats.Option{nats.Name("guidance-list"), nats.Timeout(5 * time.Second)}
		if path := os.Getenv("NATS_CREDENTIALS"); path != "" {
			options = append(options, nats.UserCredentials(path))
		}
		nc, e := nats.Connect(env("NATS_URL", "nats://127.0.0.1:4222"), options...)
		if e != nil {
			return e
		}
		defer nc.Close()
		b, _ := json.Marshal(map[string]string{"org": org})
		msg, e := nc.Request("guidance.query", b, 10*time.Second)
		if e != nil {
			return e
		}
		fmt.Println(string(msg.Data))
		return nil
	}
	if command != "serve" && command != "backup" {
		return errors.New("unknown command; use help")
	}
	store, e := guidance.Open(filepath.Join(*data, "guidance.db"), org)
	if e != nil {
		return e
	}
	defer store.DB.Close()
	if command == "backup" {
		if *output == "" {
			return errors.New("--output is required")
		}
		return store.Backup(*output)
	}
	key, e := web.SigningKey(*data)
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	base := env("BASE_URL", "https://guidance."+org+".mynimsforest.com")
	auth := web.NewAuthMiddleware(web.AuthConfig{IamNimURL: env("IAMNIM_URL", "https://iamnim.com"), BaseURL: base, OrgSlug: org})
	srv := &web.Server{Store: store, Auth: auth, Base: base, Dev: *dev, Key: key}
	if !*dev {
		opts := []nats.Option{nats.Name("nimsforestguidance"), nats.MaxReconnects(-1), nats.ReconnectWait(time.Second), nats.Timeout(10 * time.Second)}
		if path := os.Getenv("NATS_CREDENTIALS"); path != "" {
			opts = append(opts, nats.UserCredentials(path))
		}
		nc, e := nats.Connect(env("NATS_URL", "nats://127.0.0.1:4222"), opts...)
		if e != nil {
			log.Printf("organization bus unavailable; starting degraded: %v", e)
		}
		if nc != nil {
			defer nc.Close()
			srv.NC = nc
			if e = srv.StartBus(ctx); e != nil {
				log.Printf("durable guidance consumer unavailable; starting degraded: %v", e)
			}
			reg, e := tool.RegisterConn(nc, tool.Info{Name: "nimsforestguidance", OrgSlug: org, Kind: "service", Version: version, Publishes: []string{"river.guidance.forecast.changed", "humus.guidance.command.result", "tap.guidance.forecast.>"}, Subscribes: []string{"tap.guidance.forecast.>", "guidance.query"}, Tools: []tool.Definition{{Key: "guidance", Name: "Funding guidance", Description: "Versioned management forecasts and calculated funding requirements", Kind: "cli", Version: version, Assignments: []tool.Assignment{{Nim: "numbers"}, {Nim: "nurture"}, {Nim: "napoleon"}}, CLI: &tool.CLI{Executable: "nimsforestguidance", Help: []string{"help"}, Commands: []tool.Command{{Name: "list", Output: "json"}, {Name: "version", Output: "text"}}}}}})
			if e != nil {
				return e
			}
			defer reg.Stop()
		}
	}
	listen := tool.ListenAddr(*addr, "127.0.0.1:8128")
	server := &http.Server{Addr: listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		<-ctx.Done()
		stopCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		server.Shutdown(stopCtx)
	}()
	log.Printf("nimsforestguidance %s ready for %s on %s", version, org, listen)
	e = server.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
