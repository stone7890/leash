package spl_test

// The read paths, against a real public cluster.
//
// Everything else in this package runs against the local validator, where the ledger is ours and
// every account was created by the test. That proves the code is self-consistent; it does not
// prove it survives a cluster it did not build. Devnet is a different ledger, a different
// commitment cadence, a rate-limited endpoint and accounts written by strangers — which is where
// a wrong commitment level or a hopeful decode actually shows up.
//
// Skipped unless DEVNET_RPC names an endpoint, because CI must not depend on a public faucet or a
// public RPC's mood. Run it deliberately:
//
//	DEVNET_RPC=https://api.devnet.solana.com go test ./internal/chain/spl -run Devnet -v
//
// Only reads. Devnet's faucet is rate limited to the point of being unavailable, so a write test
// would be a test that fails for a reason that is not a defect.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"
	solrpc "github.com/gagliardetto/solana-go/rpc"

	"github.com/stone7890/leash/internal/chain/rpc"
	"github.com/stone7890/leash/internal/chain/spl"
	"github.com/stone7890/leash/internal/domain/network"
)

// devnetUSDC is Circle's devnet USDC. It is busy, so there is always a recent signature to read
// back, and it is a real mint rather than one this test minted into existence.
const devnetUSDC = "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"

func devnetAdapter(t *testing.T) (*spl.Adapter, *solrpc.Client) {
	t.Helper()
	endpoint := os.Getenv("DEVNET_RPC")
	if endpoint == "" {
		t.Skip("set DEVNET_RPC to run the devnet read tests")
	}
	pool, err := rpc.New(map[network.Network]rpc.Endpoints{network.Sandbox: {Primary: endpoint}})
	if err != nil {
		t.Fatal(err)
	}
	return spl.New(pool), solrpc.New(endpoint)
}

func TestDevnetSlotAndBlockhashAreReal(t *testing.T) {
	a, _ := devnetAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	slot, err := a.Slot(ctx, network.Sandbox)
	if err != nil {
		t.Fatalf("reading the slot: %v", err)
	}
	// Devnet passed 300 million in 2024. A local validator starts at zero, so this also catches
	// the test having been pointed at the wrong endpoint.
	if slot < 300_000_000 {
		t.Fatalf("slot %d is not a devnet slot", slot)
	}

	bh, err := a.Blockhash(ctx, network.Sandbox)
	if err != nil {
		t.Fatalf("reading the blockhash: %v", err)
	}
	if _, err := solana.HashFromBase58(bh); err != nil {
		t.Fatalf("blockhash %q does not decode: %v", bh, err)
	}
}

func TestDevnetAccountExistsDistinguishesRealFromAbsent(t *testing.T) {
	a, _ := devnetAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ok, err := a.AccountExists(ctx, network.Sandbox, devnetUSDC)
	if err != nil {
		t.Fatalf("reading the USDC mint: %v", err)
	}
	if !ok {
		t.Fatal("devnet USDC does not exist, which means the read path is wrong")
	}

	// A key nobody holds. `false`, and specifically not an error — the pending allowance in
	// P2 depends on absence being an ordinary answer.
	absent := solana.NewWallet().PublicKey().String()
	ok, err = a.AccountExists(ctx, network.Sandbox, absent)
	if err != nil {
		t.Fatalf("an absent account must not be an error: %v", err)
	}
	if ok {
		t.Fatalf("%s should not exist", absent)
	}
}

// An account that was never a token account must not decode into a plausible allowance. Reading a
// stranger's ledger is where an over-trusting decode turns into a wrong number on the dashboard.
func TestDevnetReadAllowanceRefusesANonTokenAccount(t *testing.T) {
	a, _ := devnetAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := a.ReadAllowance(ctx, network.Sandbox, devnetUSDC); err == nil {
		t.Fatal("a mint decoded as a token account; the decode is too permissive")
	}

	st, err := a.ReadAllowance(ctx, network.Sandbox, solana.NewWallet().PublicKey().String())
	if err != nil {
		t.Fatalf("an absent allowance must not be an error: %v", err)
	}
	if st.Exists {
		t.Fatal("an account that does not exist reported Exists")
	}
}

// The read-back, against a transaction this test did not send. Found means found; a signature
// that was never submitted is `unknown`, never `failed` (I4).
func TestDevnetSignatureStatusReadsBackAStrangersTransaction(t *testing.T) {
	a, c := devnetAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	mint := solana.MustPublicKeyFromBase58(devnetUSDC)
	limit := 5
	sigs, err := c.GetSignaturesForAddressWithOpts(ctx, mint,
		&solrpc.GetSignaturesForAddressOpts{Limit: &limit, Commitment: solrpc.CommitmentFinalized})
	if err != nil || len(sigs) == 0 {
		t.Skipf("devnet returned no recent USDC activity to read back: %v", err)
	}

	conf, err := a.SignatureStatus(ctx, network.Sandbox, sigs[0].Signature.String())
	if err != nil {
		t.Fatalf("reading back %s: %v", sigs[0].Signature, err)
	}
	if !conf.Found {
		t.Fatalf("%s is finalized on devnet but was not found", sigs[0].Signature)
	}
	if conf.Slot <= 0 {
		t.Fatalf("found at slot %d; a confirmation with no slot cannot settle a payment", conf.Slot)
	}
	if conf.Err != "" && sigs[0].Err == nil {
		t.Fatalf("reported an error (%s) for a transaction devnet says succeeded", conf.Err)
	}

	// A signature of the right shape that was never submitted. Not found, and NOT an error —
	// this is the `unknown` state the sweep re-checks rather than freeing the budget.
	var never solana.Signature
	copy(never[:], solana.NewWallet().PrivateKey[:64])
	conf, err = a.SignatureStatus(ctx, network.Sandbox, never.String())
	if err != nil {
		t.Fatalf("an unknown signature must not be an error: %v", err)
	}
	if conf.Found {
		t.Fatal("a signature that was never submitted was reported found")
	}
}
