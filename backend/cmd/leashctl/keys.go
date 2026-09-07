package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gagliardetto/solana-go"
	solrpc "github.com/gagliardetto/solana-go/rpc"

	"github.com/stone7890/leash/internal/domain/challenge"
)

// treasuryFloor is what the mint authority must keep once the sandbox exists.
//
// After bootstrap the treasury is deliberately BELOW its funding target — it just paid rent on a
// mint and a token account — so comparing it against that target for ever would report a healthy
// sandbox as broken. What it actually needs from then on is fee money: it signs one small
// transaction per faucet grant. 0.01 SOL is two thousand of them.
const treasuryFloor = uint64(solana.LAMPORTS_PER_SOL / 100)

// treasuryNeed is how much SOL the treasury must hold, and it is the ONE definition of that.
//
// Three commands ask this question — bootstrap before it funds, `keys` before it reports, `faucet`
// before it asks the cluster — and three answers would be worse than none: a faucet that tops up
// to less than bootstrap requires sends somebody round the loop again for no reason they can see.
//
// Before the sandbox exists the treasury needs its own target PLUS whatever the facilitator is
// short, because bootstrap pays the facilitator out of it. Afterwards it needs only fee money:
// a prepared sandbox sits below its funding target, having just paid rent on a mint, and treating
// that as a shortfall would report a healthy sandbox as broken.
func treasuryNeed(ctx context.Context, c *solrpc.Client, dir string, want uint64,
	prepared bool) (uint64, error) {
	if prepared {
		return treasuryFloor, nil
	}
	need := want
	facilitator, err := loadOrCreateKey(filepath.Join(dir, keyFacilit))
	if err != nil {
		return 0, err
	}
	if have := balanceOf(ctx, c, facilitator.PublicKey()); have < want {
		need += want - have
	}
	return need, nil
}

// sandboxPrepared reports whether the recorded mint is really on THIS chain.
//
// The same read-back bootstrap does, and the reason a state directory carried over from another
// ledger is not mistaken for a working sandbox.
func sandboxPrepared(ctx context.Context, c *solrpc.Client, dir string) bool {
	st, err := readState(dir)
	if err != nil {
		return false
	}
	live, err := mintExists(ctx, c, st.Mint)
	return err == nil && live
}

// cmdKeys prints the sandbox's addresses, their balances, and what is still short.
//
// It exists because of one thing a public cluster does that a local validator never does: refuse
// to fund them. On a public cluster the operator sends the SOL by hand, which means they need the address —
// and the address is otherwise only visible in the output of the bootstrap run that failed, inside
// a container, in a log that scrolls. Asking somebody to recover a base58 string from scrollback
// is asking them to mistype it.
//
// It reads the same state directory bootstrap writes, so it answers before the sandbox exists (the
// keys are created first, then funded) and after it does — and it asks the same question bootstrap
// asks, so the two cannot disagree about whether there is a problem.
func cmdKeys(ctx context.Context, _ []string) error {
	dir := env("LEASH_STATE_DIR", "/state")
	// Same directory bootstrap uses, created the same way. Running this BEFORE a first bootstrap
	// is the normal case on devnet — the operator wants the address to fund — and the keys made
	// here are the ones bootstrap will then reuse, because they are written before they are used.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	rpcURL := env("RPC_SANDBOX_URL", "http://localhost:8899")
	client := solrpc.New(rpcURL)

	want, err := fundingTarget()
	if err != nil {
		return err
	}

	c, cancel := withTimeout(ctx, 60*time.Second)
	defer cancel()

	prepared := sandboxPrepared(c, client, dir)

	fmt.Printf("sandbox keys in %s\n  chain  %s\n", dir, rpcURL)
	if prepared {
		fmt.Printf("  state  prepared — the recorded mint is on this chain\n")
	} else {
		fmt.Printf("  state  not prepared — bootstrap has not made a mint here yet\n")
	}
	fmt.Println()

	keys := []struct {
		who  string
		file string
	}{
		{"mint authority (treasury)", keyMint},
		{"faucet", keyFaucet},
		{"sample endpoint's facilitator", keyFacilit},
		{"demo endpoint's recipient", keyRecipient},
	}

	need, err := treasuryNeed(c, client, dir, want, prepared)
	if err != nil {
		return err
	}

	var treasury solana.PublicKey
	var haveTreasury uint64
	for _, k := range keys {
		w, err := loadOrCreateKey(filepath.Join(dir, k.file))
		if err != nil {
			return err
		}
		have := balanceOf(c, client, w.PublicKey())

		// Only the treasury is ever funded from outside. The facilitator is paid from it, the
		// recipient only receives, and nothing anywhere signs with the faucet key. All four are
		// listed because this is the one place that answers "what does this sandbox own" — but
		// listing an address is not the same as asking somebody to send it money.
		note := "paid from the treasury"
		switch {
		case k.file == keyRecipient:
			note = "receives only — never needs SOL"
		case k.file == keyFaucet:
			note = "never signs anything — never needs SOL"
		case k.file == keyMint:
			treasury, haveTreasury = w.PublicKey(), have
			note = "funded"
			if have < need {
				note = "NEEDS FUNDING"
			}
		}
		fmt.Printf("  %-31s %s  %9.4f SOL  %s\n", k.who, w.PublicKey(),
			float64(have)/float64(solana.LAMPORTS_PER_SOL), note)
	}

	if haveTreasury >= need {
		fmt.Printf("\nthe treasury is funded. `%s` will carry on from here.\n",
			resumeCommand(c, client))
		return nil
	}
	fmt.Printf("\nONE address to fund — bootstrap pays the others from it:\n\n")
	fmt.Printf("    send %.4f SOL to  %s\n\n",
		float64(need-haveTreasury)/float64(solana.LAMPORTS_PER_SOL), treasury)
	fmt.Println(fundingAdvice(c, client))
	fmt.Printf("Then run `%s` again — bootstrap is idempotent and carries on from here.\n",
		resumeCommand(c, client))
	return nil
}

// fundingAdvice says where the SOL can actually come from, for the cluster this really is.
//
// It matters because the answer differs and the wrong one wastes an afternoon: faucet.solana.com
// serves devnet and NOT testnet, and testnet's own RPC airdrop answers "Internal error" rather
// than refusing politely. Printing "use the faucet" on testnet would be advice that cannot work.
//
// The cluster is read from the chain's genesis hash, not from configuration — same as the way the
// sample endpoint decides what to announce, and for the same reason.
func fundingAdvice(ctx context.Context, client *solrpc.Client) string {
	switch clusterOf(ctx, client) {
	case challenge.ClusterDevnet:
		return "From https://faucet.solana.com (set it to Devnet), or any wallet holding devnet SOL."
	case challenge.ClusterTestnet:
		return "Testnet's RPC airdrop is rate limited per address and per IP, and answers 429\n" +
			"once the day's allowance is gone. https://faucet.solana.com is the alternate source\n" +
			"it names; failing that, any wallet already holding testnet SOL."
	case challenge.ClusterMainnet:
		return "This is MAINNET. Nothing here should be pointed at it — check RPC_SANDBOX_URL."
	}
	return "This ledger is not a public cluster, so it should be airdropping freely.\n" +
		"An unfunded key here usually means the validator is not the one bootstrap talked to."
}

// resumeCommand names the make target that carries on from here, for the cluster this really is.
//
// The target IS the ledger — `make devnet` sets the endpoint itself and cannot quietly run against
// testnet — so telling somebody on devnet to run `make testnet` sends them to another chain, with
// another state volume, another set of keys, and the treasury they just funded left behind on the
// ledger they were told to leave. The same reason fundingAdvice asks the chain rather than trusting
// configuration, and the same answer it asks for.
func resumeCommand(ctx context.Context, client *solrpc.Client) string {
	switch clusterOf(ctx, client) {
	case challenge.ClusterDevnet:
		return "make devnet"
	case challenge.ClusterTestnet:
		return "make testnet"
	}
	// A local validator is `make start`, and mainnet falls in with it because mainnet has no target
	// of its own and should not — fundingAdvice has already said so, loudly, one line above.
	return "make start"
}

// clusterOf asks the chain which cluster it is, from its genesis hash rather than from
// configuration — the same read the sample endpoint makes to decide what to announce. An empty
// cluster means "not a public one", which for this binary means the local validator.
func clusterOf(ctx context.Context, client *solrpc.Client) challenge.Cluster {
	if h, err := client.GetGenesisHash(ctx); err == nil {
		if got, ok := challenge.ClusterFromGenesis(h.String()); ok {
			return got
		}
	}
	return challenge.Cluster("")
}
