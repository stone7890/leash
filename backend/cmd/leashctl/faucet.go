package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gagliardetto/solana-go"
	solrpc "github.com/gagliardetto/solana-go/rpc"

	"github.com/stone7890/leash/internal/domain/money"
)

// cmdFaucet asks the sandbox's chain for SOL, and optionally mints test USDC.
//
// It is the one step of bootstrap worth doing on its own. On a local validator bootstrap never
// stops for funding, so this is never needed; on a public cluster funding is the step that fails,
// and re-running the whole of bootstrap to retry one airdrop means waiting through migrations and
// a mint check to find out whether the faucet's mood has changed. `leashctl faucet` asks the one
// question.
//
// It defaults to the treasury because that is the address bootstrap is blocked on. Given an
// address it funds that instead, which is what makes it useful after the sandbox exists: a wallet
// used from the dashboard needs SOL for fees and USDC to delegate, and on a public cluster neither
// arrives by itself.
//
// SOL and USDC come from different places and fail differently, which is why both are reported
// separately. The SOL is the CLUSTER's, handed out by a faucet that may refuse. The USDC is OURS,
// minted by the authority bootstrap created — it cannot be rate limited, and it works on any
// cluster, which is the whole reason the sandbox does not depend on a published test token.
func cmdFaucet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("faucet", flag.ContinueOnError)
	solAmount := fs.Float64("sol", 0, "SOL to request (default: enough to reach BOOTSTRAP_FUND_SOL)")
	usdcAmount := fs.Float64("usdc", 0, "test USDC to mint (requires a prepared sandbox)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dir := env("LEASH_STATE_DIR", "/state")
	rpcURL := env("RPC_SANDBOX_URL", "http://localhost:8899")
	client := solrpc.New(rpcURL)

	c, cancel := withTimeout(ctx, 3*time.Minute)
	defer cancel()

	authority, err := loadOrCreateKey(filepath.Join(dir, keyMint))
	if err != nil {
		return err
	}

	// No address means the treasury, because that is what bootstrap stops for.
	target := authority.PublicKey()
	if rest := fs.Args(); len(rest) > 0 {
		target, err = solana.PublicKeyFromBase58(rest[0])
		if err != nil {
			return fmt.Errorf("%q is not a Solana address", rest[0])
		}
	}
	isTreasury := target.Equals(authority.PublicKey())

	want, err := fundingTarget()
	if err != nil {
		return err
	}
	// The treasury's target is not its own balance target: bootstrap pays the facilitator out of
	// it, so topping up to less than bootstrap requires would send somebody round the loop again
	// for no reason they could see. Same function bootstrap and `keys` use.
	if isTreasury {
		want, err = treasuryNeed(c, client, dir, want, sandboxPrepared(c, client, dir))
		if err != nil {
			return err
		}
	}
	have := balanceOf(c, client, target)
	fmt.Printf("chain    %s\naddress  %s%s\nbalance  %.4f SOL\n\n", rpcURL, target,
		map[bool]string{true: "   (the treasury)"}[isTreasury],
		float64(have)/float64(solana.LAMPORTS_PER_SOL))

	lamports := uint64(0)
	switch {
	case *solAmount > 0:
		lamports = uint64(*solAmount * float64(solana.LAMPORTS_PER_SOL))
	case have < want:
		lamports = want - have
	}

	var failed error
	if lamports == 0 {
		fmt.Printf("SOL      already at %.4f, which meets the %.4f target — not asking\n",
			float64(have)/float64(solana.LAMPORTS_PER_SOL),
			float64(want)/float64(solana.LAMPORTS_PER_SOL))
	} else {
		failed = requestSOL(c, client, target, lamports)
	}

	if *usdcAmount > 0 {
		if err := mintTestUSDC(c, client, dir, authority, target,
			money.Base(int64(*usdcAmount*float64(money.One)))); err != nil {
			return err
		}
	}

	if failed != nil {
		fmt.Println()
		fmt.Println(fundingAdvice(c, client))
		return errors.New("the cluster would not hand out SOL")
	}
	return nil
}

// requestSOL asks for an airdrop and reads the result back rather than assuming it landed.
//
// A rate limit is reported as the expected answer it is, not as a fault: a public faucet refusing
// is the normal case, and dressing it up as an RPC failure trains people to ignore both.
func requestSOL(ctx context.Context, c *solrpc.Client, target solana.PublicKey,
	lamports uint64) error {
	fmt.Printf("SOL      asking for %.4f…\n", float64(lamports)/float64(solana.LAMPORTS_PER_SOL))
	sig, err := c.RequestAirdrop(ctx, target, lamports, solrpc.CommitmentConfirmed)
	if err == nil {
		if err = waitForSignature(ctx, c, sig); err == nil {
			fmt.Printf("         granted · %s\n         balance now %.4f SOL\n", sig,
				float64(balanceOf(ctx, c, target))/float64(solana.LAMPORTS_PER_SOL))
			return nil
		}
	}
	if rateLimited(err) {
		fmt.Printf("         refused — the cluster's faucet is rate limited, as expected\n")
	} else {
		fmt.Printf("         refused — %s\n", rpcMessage(err))
	}
	return err
}

// mintTestUSDC mints the sandbox's own token, which no faucet can refuse.
func mintTestUSDC(ctx context.Context, c *solrpc.Client, dir string, authority *solana.Wallet,
	target solana.PublicKey, amount money.Base) error {
	st, err := readState(dir)
	if err != nil {
		return fmt.Errorf("there is no sandbox here yet, so there is no mint to draw on: %w", err)
	}
	live, err := mintExists(ctx, c, st.Mint)
	if err != nil {
		return err
	}
	if !live {
		return fmt.Errorf("the recorded mint %s is not on this chain — run bootstrap first", st.Mint)
	}
	mint, err := solana.PublicKeyFromBase58(st.Mint)
	if err != nil {
		return err
	}
	fmt.Printf("USDC     minting %s of %s…\n", amount, st.Mint)
	ata, err := fundWithUSDC(ctx, c, authority, target, mint, amount)
	if err != nil {
		return fmt.Errorf("minting test USDC: %w", err)
	}
	fmt.Printf("         minted into %s\n", ata)
	return nil
}
