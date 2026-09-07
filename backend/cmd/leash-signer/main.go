// leash-signer holds every agent key, evaluates the eight rules, and signs.
//
// It is the only process linked against internal/kms, and the only one that produces a signature.
// The dashboard has no route to it.
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stone7890/leash/internal/chain"
	"github.com/stone7890/leash/internal/chain/rpc"
	"github.com/stone7890/leash/internal/chain/spl"
	"github.com/stone7890/leash/internal/config"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/kms"
	"github.com/stone7890/leash/internal/migrate"
	"github.com/stone7890/leash/internal/obs"
	"github.com/stone7890/leash/internal/signer"
	"github.com/stone7890/leash/internal/signer/cache"
	"github.com/stone7890/leash/internal/signer/hot"
	"github.com/stone7890/leash/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		// Printed as well as logged: a container log reader may not have a level configured, and a
		// boot failure nobody can read is a boot failure nobody can fix.
		fmt.Fprintf(os.Stderr, "\n%v\n\n", err)
		slog.Error("leash-signer failed to start", "err", err)
		os.Exit(1)
	}
}

// The order below is load-bearing. Everything that can fail is given the chance to fail BEFORE the
// port is bound: a service that starts serving and then discovers it cannot reach its database
// reports itself healthy while being useless, and a load balancer believes it.
func run(ctx context.Context) error {
	// 1 · configuration. Half-configured fails here, naming everything that is missing at once.
	cfg, err := config.LoadSigner()
	if err != nil {
		return err
	}
	obs.Setup(cfg.LogLevel, "leash-signer")
	slog.Info("starting", "networks", cfg.Networks)

	// 2 · the database, then the migrations. Before anything else.
	st, err := store.Connect(ctx, cfg.MongoURI, cfg.MongoDB)
	if err != nil {
		return err
	}
	defer st.Close(context.Background())
	if err := migrate.Apply(ctx, st.Migrator()); err != nil {
		return err
	}

	// 3 · key custody.
	stateDir := envOr("LEASH_STATE_DIR", "/state")
	var wrapper kms.Wrapper
	if cfg.KMSLocal {
		// Refused alongside mainnet by the configuration itself: a locally-wrapped key protecting
		// real money is exactly the shortcut that should not be one flag away.
		slog.Warn("wrapping agent keys with a LOCAL key — sandbox only", "state_dir", stateDir)
		wrapper, err = kms.NewLocal(stateDir)
	} else {
		return errors.New(
			"AWS KMS wrapping is not implemented in this build. Set KMS_LOCAL=true for the " +
				"sandbox, which the configuration already refuses to combine with mainnet")
	}
	if err != nil {
		return err
	}

	pool, err := rpc.New(rpcEndpoints(cfg))
	if err != nil {
		return err
	}
	adapter := spl.New(pool)

	// 4 · the caches.
	snapshots := cache.NewSnapshots(st, cfg.SnapshotTTL)
	keys := cache.NewKeys(unwrapper(st, wrapper), cfg.KeyCacheTTL)
	defer keys.ZeroAll()
	blocks := cache.NewBlockhashes(cfg.BlockhashTTL)

	timeline := signer.NewTimeline(st, 4096)
	go timeline.Run(ctx, 2)

	// 5 · warm the blockhash BEFORE binding. A cold one on the first signature is a p99 spike on
	//     the first payment a new customer makes, which is the demo.
	go blocks.Refresh(ctx, cfg.Networks, func(c context.Context, n network.Network) (string, error) {
		return adapter.Blockhash(c, n)
	})
	if err := waitForBlockhash(ctx, blocks, cfg.Networks); err != nil {
		return err
	}

	// 6 · the kill switch's "instantly". A change stream evicts within milliseconds; the TTL is
	//     only the floor beneath it.
	go watchKills(ctx, st, snapshots, keys)

	mints := map[network.Network]string{}
	for n, m := range cfg.Mints {
		mints[n] = m.Address
	}
	h := hot.New(st, keys, snapshots, blocks, timeline, adapter, mints, func() time.Time {
		return time.Now().UTC()
	})

	// Agent key provisioning, on an internal path. The dashboard cannot mint a keypair — nothing
	// under frontend/website may import a KMS client — so it asks the one process that holds keys.
	prov := signer.NewProvisioner(st, wrapper, os.Getenv("INTERNAL_TOKEN"))

	// 7 · bind, last.
	srv := &http.Server{
		Handler:      signer.Router(h, prov, health(blocks, timeline, cfg.Networks)),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
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
	timeline.Wait()
	slog.Info("stopped cleanly")
	return nil
}

// unwrapper reads the wrapped key and returns a usable one. It is the only path back to key
// material, and it exists behind an interface so the hot path is handed the ability to sign rather
// than the key store itself.
func unwrapper(st *store.Store, w kms.Wrapper) cache.UnwrapFunc {
	return func(ctx context.Context, agentID string) (ed25519.PrivateKey, error) {
		wrapped, keyRef, err := st.WrappedKey(ctx, agentID)
		if err != nil {
			return nil, err
		}
		return w.Unwrap(wrapped, keyRef)
	}
}

func waitForBlockhash(ctx context.Context, b *cache.Blockhashes, nets []network.Network) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		ready := true
		for _, n := range nets {
			if _, ok := b.Get(n); !ok {
				ready = false
			}
		}
		if ready {
			return nil
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.New(
		"no recent blockhash after 60s — the RPC endpoint is unreachable. Refusing to serve: a " +
			"signer with no blockhash would refuse every payment anyway, and doing so while " +
			"reporting itself healthy is worse")
}

// watchKills evicts a killed agent within milliseconds.
//
// A 60-second TTL is far too slow for rule S7, whose whole promise — the sentence an owner reads
// before confirming — is that the signer refuses every new payment INSTANTLY.
//
// If the stream drops, the TTL is the floor. That is a degraded state, not a silent one: it logs
// at error level, and the kill switch still works, up to a minute slower.
func watchKills(ctx context.Context, st *store.Store, snaps *cache.Snapshots, keys *cache.Keys) {
	for ctx.Err() == nil {
		err := st.WatchAgentKills(ctx, func(agentID string) {
			slog.Info("evicting a killed or rotated agent", "agent_id", agentID)
			snaps.Evict(agentID)
			keys.Evict(agentID)
		})
		if ctx.Err() != nil {
			return
		}
		slog.Error("the kill-switch change stream stopped — falling back to the cache TTL, "+
			"which makes the kill switch up to a minute slower", "err", err)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func health(b *cache.Blockhashes, t *signer.Timeline, nets []network.Network) func() map[string]any {
	return func() map[string]any {
		bh := map[string]bool{}
		for _, n := range nets {
			_, ok := b.Get(n)
			bh[n.String()] = ok
		}
		return map[string]any{
			"status":           "ok",
			"blockhash_ready":  bh,
			"timeline_dropped": t.Dropped(),
		}
	}
}

func rpcEndpoints(cfg config.Signer) map[network.Network]rpc.Endpoints {
	out := map[network.Network]rpc.Endpoints{}
	for n, e := range cfg.RPC {
		out[n] = rpc.Endpoints{Primary: e.Primary, Fallback: e.Fallback}
	}
	return out
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

var _ = chain.Adapter(nil)
