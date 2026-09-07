package indexer

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"encoding/json"

	"github.com/gagliardetto/solana-go"
	ata "github.com/gagliardetto/solana-go/programs/associated-token-account"
	"github.com/gagliardetto/solana-go/programs/token"
	solrpc "github.com/gagliardetto/solana-go/rpc"
	"github.com/gin-gonic/gin"
	"github.com/stone7890/leash/internal/chain/rpc"
	"github.com/stone7890/leash/internal/domain/fault"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
)

// The sandbox faucet.
//
// A brand-new owner has no sandbox USDC and no SOL, so without this the onboarding wizard stops at
// step 3 with a transaction that cannot pay its own fee. The specification's step N-01 is explicit:
// the workspace starts on the sandbox and the faucet grants 100 test USDC.
//
// Sandbox ONLY. There is no mainnet branch here at all — not a check that returns an error, but no
// code path that could mint anything on a network where money is real.

// FaucetGrant is what one request hands out: enough SOL for many transactions, and the 100 test
// USDC the specification names.
const (
	faucetSOL  = uint64(2 * solana.LAMPORTS_PER_SOL)
	faucetUSDC = 100 * money.One

	// faucetSOLFloor is what a wallet must already hold for a refused airdrop to be survivable.
	//
	// A local validator airdrops freely; devnet's faucet is rate limited and frequently dry, so on
	// a public sandbox the request WILL be refused sometimes. A refusal is only a failure if the
	// wallet cannot pay for what comes next — and 0.05 SOL is hundreds of signatures, far more
	// than the wizard's delegation and its payments need.
	faucetSOLFloor = uint64(solana.LAMPORTS_PER_SOL / 20)
)

func (ix *Indexer) faucet(c *gin.Context) {
	if !ix.internalOK(c) {
		c.JSON(http.StatusUnauthorized, fault.Envelope{Error: fault.Body{
			Code: "UNAUTHENTICATED", Message: "internal token required", Retriable: false}})
		return
	}
	var body struct {
		Wallet  string `json:"wallet"`
		Network string `json:"network"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Wallet == "" {
		c.JSON(http.StatusBadRequest, fault.Envelope{Error: fault.Body{
			Code: "MALFORMED_REQUEST", Message: "a wallet is required", Retriable: false}})
		return
	}
	if body.Network != string(network.Sandbox) {
		// Nonsensical rather than forbidden: there is no such thing as free mainnet USDC.
		c.JSON(http.StatusUnprocessableEntity, fault.Envelope{Error: fault.Body{
			Code:    "MAINNET_ONLY_OPERATION",
			Message: "the faucet exists on the sandbox only", Retriable: false}})
		return
	}

	owner, err := solana.PublicKeyFromBase58(body.Wallet)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, fault.Envelope{Error: fault.Body{
			Code: "INVALID_WALLET", Message: "not a Solana address", Field: "wallet",
			Retriable: false}})
		return
	}

	if err := ix.grant(c.Request.Context(), owner); err != nil {
		c.JSON(http.StatusServiceUnavailable, fault.Envelope{Error: fault.Body{
			Code: "CHAIN_UNREACHABLE", Message: err.Error(), Retriable: true}})
		return
	}
	// 202: the transfers are submitted. The wallet's balance is the chain's business, and the
	// interface reads it back rather than being told here that it changed.
	c.JSON(http.StatusAccepted, gin.H{
		"sol":  fmt.Sprintf("%.4f", float64(faucetSOL)/float64(solana.LAMPORTS_PER_SOL)),
		"usdc": faucetUSDC.String(),
	})
}

// grant airdrops SOL and mints test USDC into the owner's associated token account.
//
// The associated one specifically: that is where a wallet puts a user's USDC and therefore where
// the delegation transaction expects to find it.
func (ix *Indexer) grant(ctx context.Context, owner solana.PublicKey) error {
	authority, err := ix.mintAuthority()
	if err != nil {
		return err
	}
	mintAddr, _ := ix.mintFor(network.Sandbox)
	mint, err := solana.PublicKeyFromBase58(mintAddr)
	if err != nil {
		return fmt.Errorf("the sandbox mint is not configured: %w", err)
	}

	// SOL first: without it the owner cannot pay the fee on the delegation, and a wallet holding
	// USDC it cannot move is a worse experience than one holding nothing.
	if err := ix.airdrop(ctx, owner); err != nil {
		return err
	}

	ataAddr, _, err := solana.FindAssociatedTokenAddress(owner, mint)
	if err != nil {
		return err
	}

	bh, err := rpc.Do(ctx, ix.pool, network.Sandbox,
		func(cl *solrpc.Client) (solana.Hash, error) {
			out, err := cl.GetLatestBlockhash(ctx, solrpc.CommitmentConfirmed)
			if err != nil {
				return solana.Hash{}, err
			}
			return out.Value.Blockhash, nil
		})
	if err != nil {
		return err
	}

	tx, err := solana.NewTransaction([]solana.Instruction{
		// Idempotent: a second faucet call must not fail because the account already exists.
		ata.NewCreateIdempotentInstruction(authority.PublicKey(), owner, mint).Build(),
		token.NewMintToInstruction(uint64(faucetUSDC), mint, ataAddr,
			authority.PublicKey(), nil).Build(),
	}, bh, solana.TransactionPayer(authority.PublicKey()))
	if err != nil {
		return err
	}
	if _, err := tx.Sign(func(k solana.PublicKey) *solana.PrivateKey {
		if k.Equals(authority.PublicKey()) {
			return &authority.PrivateKey
		}
		return nil
	}); err != nil {
		return err
	}

	sig, err := rpc.Do(ctx, ix.pool, network.Sandbox,
		func(cl *solrpc.Client) (solana.Signature, error) {
			return cl.SendTransactionWithOpts(ctx, tx, solrpc.TransactionOpts{
				PreflightCommitment: solrpc.CommitmentConfirmed,
			})
		})
	if err != nil {
		return fmt.Errorf("minting test USDC: %w", err)
	}

	// Wait for it: the wizard's next step spends this, and handing back before it lands would send
	// the owner into a transaction that fails for a reason they cannot see.
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		out, err := rpc.Do(ctx, ix.pool, network.Sandbox,
			func(cl *solrpc.Client) (*solrpc.GetSignatureStatusesResult, error) {
				return cl.GetSignatureStatuses(ctx, true, sig)
			})
		if err == nil && len(out.Value) > 0 && out.Value[0] != nil {
			if out.Value[0].Err != nil {
				return fmt.Errorf("the faucet transfer failed: %v", out.Value[0].Err)
			}
			return nil
		}
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("the faucet transfer did not confirm within 45s")
}

// airdrop tops the owner's wallet up with SOL, and settles for what it already holds when the
// network refuses.
//
// The same judgement `leashctl bootstrap` makes, for the same reason: the sandbox ledger may be a
// local validator, which airdrops on demand, or devnet, whose faucet is rate limited and often dry
// (https://faucet.solana.com). Treating every refusal as a failure would make the faucet unusable
// on devnet for a wallet that is already perfectly able to transact — so the question asked is not
// "did the airdrop succeed" but "can this wallet pay its way", which is the one that matters.
//
// It fails only when both are false, and then it says the address and the amount, because "airdrop
// failed" leaves an owner guessing and "send SOL to this address" does not.
func (ix *Indexer) airdrop(ctx context.Context, owner solana.PublicKey) error {
	balance := func() uint64 {
		out, err := rpc.Do(ctx, ix.pool, network.Sandbox,
			func(cl *solrpc.Client) (*solrpc.GetBalanceResult, error) {
				return cl.GetBalance(ctx, owner, solrpc.CommitmentConfirmed)
			})
		if err != nil || out == nil {
			return 0
		}
		return out.Value
	}

	// Already comfortable: asking anyway spends a rate limit that another owner needs.
	if balance() >= faucetSOL {
		return nil
	}

	_, err := rpc.Do(ctx, ix.pool, network.Sandbox,
		func(cl *solrpc.Client) (solana.Signature, error) {
			return cl.RequestAirdrop(ctx, owner, faucetSOL, solrpc.CommitmentConfirmed)
		})
	if err == nil {
		return nil
	}
	if balance() >= faucetSOLFloor {
		return nil
	}
	return fmt.Errorf("this network will not fund %s, and it holds almost nothing. A local "+
		"validator airdrops freely; a public one does not — send at least %.2f SOL to that "+
		"address (https://faucet.solana.com on devnet) and try again. The test USDC is minted by "+
		"this faucet and does not depend on it. (airdrop: %v)",
		owner, float64(faucetSOLFloor)/float64(solana.LAMPORTS_PER_SOL), err)
}

// mintAuthority reads the key `leashctl bootstrap` created. It is the sandbox's mint authority and
// nothing else — it has no power over any real token.
func (ix *Indexer) mintAuthority() (*solana.Wallet, error) {
	dir := os.Getenv("LEASH_STATE_DIR")
	if dir == "" {
		dir = "/state"
	}
	b, err := os.ReadFile(filepath.Join(dir, "mint-authority.json"))
	if err != nil {
		return nil, fmt.Errorf("the sandbox mint authority is not available: %w", err)
	}
	var raw []byte
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	return &solana.Wallet{PrivateKey: solana.PrivateKey(raw)}, nil
}
