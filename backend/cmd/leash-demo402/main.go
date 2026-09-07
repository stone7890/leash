// leash-demo402 is the sample x402 endpoint for onboarding step 5.
//
// Sandbox only. Its configuration refuses to boot with mainnet in NETWORKS, because nothing it
// returns is worth real money.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/stone7890/leash/internal/chain/rpc"
	"github.com/stone7890/leash/internal/config"
	"github.com/stone7890/leash/internal/demo402"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/obs"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "\n%v\n\n", err)
		slog.Error("leash-demo402 failed to start", "err", err)
		os.Exit(1)
	}
}

// loadFacilitator reads the key `leashctl bootstrap` created for this endpoint.
//
// It is the sandbox's fee payer and nothing else: it holds a little SOL and has no authority over
// anybody's USDC. A real deployment would put this behind a KMS the way the signer does — the
// interface is a byte-blob signature either way.
func loadFacilitator() (*solana.Wallet, error) {
	dir := os.Getenv("LEASH_STATE_DIR")
	if dir == "" {
		dir = "/state"
	}
	b, err := os.ReadFile(filepath.Join(dir, "facilitator.json"))
	if err != nil {
		return nil, fmt.Errorf(
			"the facilitator key is not available (%w). Run `leashctl bootstrap` first: this "+
				"endpoint sponsors the network fee, so it cannot settle without one", err)
	}
	var raw []byte
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	return &solana.Wallet{PrivateKey: solana.PrivateKey(raw)}, nil
}

func run(ctx context.Context) error {
	cfg, err := config.LoadDemo402()
	if err != nil {
		return err
	}
	obs.Setup(cfg.LogLevel, "leash-demo402")

	pool, err := rpc.New(map[network.Network]rpc.Endpoints{
		network.Sandbox: {
			Primary:  cfg.RPC[network.Sandbox].Primary,
			Fallback: cfg.RPC[network.Sandbox].Fallback,
		},
	})
	if err != nil {
		return err
	}

	// This endpoint is its own facilitator: it sponsors the network fee and co-signs settlement.
	// pay-kit works the same way — its Go adapter refuses a FacilitatorURL outright, so a server
	// either does this itself or does not settle at all.
	facilitator, err := loadFacilitator()
	if err != nil {
		return err
	}
	slog.Info("acting as the facilitator", "fee_payer", facilitator.PublicKey())

	srv := &http.Server{
		Handler: demo402.New(pool, demo402.Options{
			Mint:         cfg.Mints[network.Sandbox].Address,
			PayTo:        cfg.PayTo,
			Price:        money.Base(cfg.PriceBase),
			FeePayer:     facilitator.PublicKey().String(),
			TokenProgram: cfg.Mints[network.Sandbox].ProgramID,
			Sign: func(message []byte) ([]byte, error) {
				sig, err := facilitator.PrivateKey.Sign(message)
				if err != nil {
					return nil, err
				}
				return sig[:], nil
			},
		}).Router(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.Bind)
	if err != nil {
		return fmt.Errorf("could not bind %s: %w", cfg.Bind, err)
	}
	slog.Info("listening", "bind", cfg.Bind, "price", money.Base(cfg.PriceBase).String(),
		"pay_to", cfg.PayTo)

	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(sc)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	slog.Info("stopped cleanly")
	return nil
}
