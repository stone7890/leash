// leash-indexer reads the chain back and reconciles.
//
// It is the only component that polls outward and the only writer of `confirmed`. It is a separate
// long-running process because its work is loops on a 30-second cadence, not requests — and because
// the onboarding stream needs a change stream held open for a whole wizard session.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stone7890/leash/internal/chain/rpc"
	"github.com/stone7890/leash/internal/chain/spl"
	"github.com/stone7890/leash/internal/config"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/indexer"
	"github.com/stone7890/leash/internal/jobs"
	"github.com/stone7890/leash/internal/migrate"
	"github.com/stone7890/leash/internal/obs"
	"github.com/stone7890/leash/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "\n%v\n\n", err)
		slog.Error("leash-indexer failed to start", "err", err)
		os.Exit(1)
	}
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func run(ctx context.Context) error {
	cfg, err := config.LoadIndexer()
	if err != nil {
		return err
	}
	obs.Setup(cfg.LogLevel, "leash-indexer")
	slog.Info("starting", "networks", cfg.Networks)

	st, err := store.Connect(ctx, cfg.MongoURI, cfg.MongoDB)
	if err != nil {
		return err
	}
	defer st.Close(context.Background())
	if err := migrate.Apply(ctx, st.Migrator()); err != nil {
		return err
	}

	endpoints := map[network.Network]rpc.Endpoints{}
	for n, e := range cfg.RPC {
		endpoints[n] = rpc.Endpoints{Primary: e.Primary, Fallback: e.Fallback}
	}
	pool, err := rpc.New(endpoints)
	if err != nil {
		return err
	}
	adapter := spl.New(pool)

	// Report what each chain says, without making it fatal. A transient RPC outage should not keep
	// the read paths down; it should be a loud warning and a degraded banner.
	for _, n := range cfg.Networks {
		if slot, err := adapter.Slot(ctx, n); err == nil {
			slog.Info("chain reachable", "network", n, "slot", slot)
		} else {
			slog.Warn("chain unreachable — read-back will lag until it returns",
				"network", n, "err", err)
		}
	}

	mints := map[network.Network]indexer.MintConfig{}
	for n, m := range cfg.Mints {
		mints[n] = indexer.MintConfig{Address: m.Address, Program: m.ProgramID}
	}
	ix := indexer.New(st, adapter, indexer.Options{
		Pool:          pool,
		InternalToken: os.Getenv("INTERNAL_TOKEN"),
		SignerURL:     envOr("SIGNER_BASE_URL", "http://signer:4100"),
		DemoURL:       cfg.DemoEndpointURL,
		Mints:         mints,
	})
	sup := jobs.Start(ctx, ix.Jobs(cfg.Networks,
		cfg.AllowanceRefresh, cfg.PaymentSweep, cfg.AlertEval, cfg.FaucetGuard))

	srv := &http.Server{
		Handler:     ix.Router(func() map[string]any { return map[string]any{"status": "ok"} }),
		ReadTimeout: 10 * time.Second,
		// No write timeout: the stream endpoint holds a connection for the length of an onboarding
		// session, and a timeout here would cut the log off mid-payment.
	}
	ln, err := net.Listen("tcp", cfg.Bind)
	if err != nil {
		return fmt.Errorf("could not bind %s: %w", cfg.Bind, err)
	}
	slog.Info("listening", "bind", cfg.Bind)

	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	// Let an in-flight read-back finish and write its result, rather than losing it.
	sup.Wait()
	slog.Info("stopped cleanly")
	return nil
}
