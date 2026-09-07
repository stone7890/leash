// Package spl implements the allowance adapter on the standard SPL Token program.
//
// Leash writes no program. This uses `approve` and `revoke`, which have existed and been audited
// for years, and that is the whole of the on-chain surface.
//
// THE CONSTRAINT THAT SHAPES EVERYTHING HERE: `approve` sets THE delegate on a token account —
// singular. A second approve replaces the first. So an owner with three agents cannot delegate
// three allowances from one USDC account; agent two would silently take agent one's delegation.
//
// The resolution is a token account PER AGENT, owned by the owner, funded to the cap:
//
//  1. create the agent's token account       owner-owned, agent-specific
//  2. transfer `cap` USDC into it            from the owner's main account
//  3. approve the agent's pubkey as delegate for exactly `cap`
//
// Four consequences follow, and none is optional. The owner's funds MOVE — into an account they
// still own, so invariant I1 holds, but the onboarding copy must say so rather than claiming
// "funds stay put". Rent is payable per agent and is shown before signing. Revoke is TWO
// instructions — revoke and sweep — or the owner strands their own balance in an account they will
// not think to look at. And a top-up REPLACES the delegation rather than adding to it, which is
// why an allowance carries an epoch.
//
// What this implementation does NOT enforce on-chain is EXPIRY: the SPL delegate has a delegated
// amount and no clock. Expiry is therefore a signer-tier rule, Capabilities says so, and the
// interface reads the tier from the allowance rather than from a constant. Invariant I3 does not
// permit us to draw it as chain-enforced.
package spl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	bin "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
	ata "github.com/gagliardetto/solana-go/programs/associated-token-account"
	"github.com/gagliardetto/solana-go/programs/token"
	solrpc "github.com/gagliardetto/solana-go/rpc"
	"github.com/stone7890/leash/internal/chain"
	"github.com/stone7890/leash/internal/chain/rpc"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/domain/state"
)

type Adapter struct {
	pool *rpc.Pool
}

func New(pool *rpc.Pool) *Adapter { return &Adapter{pool: pool} }

// Capabilities is what this implementation ACTUALLY enforces. It is written to each allowance when
// it is created, and the interface renders the tier from that stored value.
func (a *Adapter) Capabilities() chain.Capabilities {
	return chain.Capabilities{
		Cap:                   true,
		Revoke:                true,
		Expiry:                false, // the SPL delegate has no clock
		OneDelegatePerAccount: true,
	}
}

// AllowanceAccount derives the per-agent token account deterministically.
//
// It uses CreateAccountWithSeed rather than a program-derived address, and the distinction is
// load-bearing. A PDA cannot sign, so the system program's CreateAccount — which requires the new
// account to be a signer — cannot make one. CreateWithSeed gives the same property that matters
// here (the same owner and agent always yield the same address, so a delegation whose confirmation
// we missed is recognised rather than duplicated) while needing only the OWNER's signature.
//
// The owner is the base and the account's authority throughout. Leash never holds a key to it,
// which is what keeps invariant I1 true while still giving each agent an account of its own.
func (a *Adapter) AllowanceAccount(_ context.Context, owner, agent, mint string,
	_ network.Network) (string, error) {
	ownerKey, err := solana.PublicKeyFromBase58(owner)
	if err != nil {
		return "", fmt.Errorf("owner address: %w", err)
	}
	addr, err := solana.CreateWithSeed(ownerKey, AllowanceSeed(agent, mint), solana.TokenProgramID)
	if err != nil {
		return "", fmt.Errorf("deriving the allowance account: %w", err)
	}
	return addr.String(), nil
}

// AllowanceSeed is the seed string for a given agent and mint.
//
// Solana caps a seed at 32 bytes, and an agent public key is 44 base58 characters — so it is
// hashed. The hash covers the mint as well, because an agent may hold one allowance per mint and
// two of them must not collide onto the same account.
func AllowanceSeed(agent, mint string) string {
	sum := sha256.Sum256([]byte("leash-allowance|" + agent + "|" + mint))
	return hex.EncodeToString(sum[:])[:32]
}

func (a *Adapter) Slot(ctx context.Context, n network.Network) (int64, error) {
	return rpc.Do(ctx, a.pool, n, func(c *solrpc.Client) (int64, error) {
		s, err := c.GetSlot(ctx, solrpc.CommitmentConfirmed)
		return int64(s), err
	})
}

func (a *Adapter) Blockhash(ctx context.Context, n network.Network) (string, error) {
	return rpc.Do(ctx, a.pool, n, func(c *solrpc.Client) (string, error) {
		out, err := c.GetLatestBlockhash(ctx, solrpc.CommitmentConfirmed)
		if err != nil {
			return "", err
		}
		return out.Value.Blockhash.String(), nil
	})
}

func (a *Adapter) AccountExists(ctx context.Context, n network.Network, account string) (bool, error) {
	key, err := solana.PublicKeyFromBase58(account)
	if err != nil {
		return false, err
	}
	return rpc.Do(ctx, a.pool, n, func(c *solrpc.Client) (bool, error) {
		_, err := c.GetAccountInfo(ctx, key)
		if errors.Is(err, solrpc.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil
	})
}

// ReadAllowance is the read-back. Every value it returns carries the slot it was read at, because
// a balance without a slot is a balance we cannot justify (I2).
func (a *Adapter) ReadAllowance(ctx context.Context, n network.Network, account string) (
	chain.AllowanceState, error) {
	key, err := solana.PublicKeyFromBase58(account)
	if err != nil {
		return chain.AllowanceState{}, err
	}

	return rpc.Do(ctx, a.pool, n, func(c *solrpc.Client) (chain.AllowanceState, error) {
		res, err := c.GetAccountInfoWithOpts(ctx, key, &solrpc.GetAccountInfoOpts{
			Commitment: solrpc.CommitmentConfirmed,
		})
		if errors.Is(err, solrpc.ErrNotFound) {
			// Not an error, and NOT a failure. The delegation has not landed yet, so the allowance
			// stays pending — which is exactly what I4 asks for.
			return chain.AllowanceState{Exists: false}, nil
		}
		if err != nil {
			return chain.AllowanceState{}, err
		}
		if res == nil || res.Value == nil {
			return chain.AllowanceState{Exists: false}, nil
		}

		var acc token.Account
		if err := bin.NewBinDecoder(res.Value.Data.GetBinary()).Decode(&acc); err != nil {
			return chain.AllowanceState{}, fmt.Errorf("decoding the token account: %w", err)
		}

		out := chain.AllowanceState{
			Exists: true,
			Slot:   int64(res.Context.Slot),
		}

		// The delegated amount IS the remaining cap: SPL decrements it as the delegate spends. So
		// `drawn` is what has gone, and the original cap is what the caller already knows.
		if acc.Delegate == nil || acc.DelegatedAmount == 0 {
			// No delegate, or nothing left to draw. Either the owner revoked, or the cap is spent.
			out.State = state.AllowanceRevoked
			if acc.Delegate != nil {
				out.State = state.AllowanceActive
			}
			out.Cap = 0
			out.Drawn = 0
			return out, nil
		}
		out.State = state.AllowanceActive
		out.Cap = money.Base(acc.DelegatedAmount)
		return out, nil
	})
}

// SignatureStatus is the read-back by signature.
//
// `Found` false does NOT mean failed. It means we have not seen it, which is the `unknown` state —
// a first-class outcome that keeps holding its money and is re-checked, because assuming failure
// and freeing the budget is how one challenge gets paid twice.
func (a *Adapter) SignatureStatus(ctx context.Context, n network.Network, signature string) (
	chain.Confirmation, error) {
	sig, err := solana.SignatureFromBase58(signature)
	if err != nil {
		return chain.Confirmation{}, err
	}
	return rpc.Do(ctx, a.pool, n, func(c *solrpc.Client) (chain.Confirmation, error) {
		out, err := c.GetSignatureStatuses(ctx, true, sig)
		if err != nil {
			return chain.Confirmation{}, err
		}
		if len(out.Value) == 0 || out.Value[0] == nil {
			return chain.Confirmation{Found: false}, nil
		}
		st := out.Value[0]
		conf := chain.Confirmation{Found: true, Slot: int64(st.Slot)}
		if st.Err != nil {
			conf.Err = fmt.Sprint(st.Err)
			return conf, nil
		}
		if st.ConfirmationStatus == solrpc.ConfirmationStatusConfirmed ||
			st.ConfirmationStatus == solrpc.ConfirmationStatusFinalized {
			conf.Confirmed = true
		}
		return conf, nil
	})
}

// rentForTokenAccount is the exemption a token account needs. It is shown to the owner BEFORE they
// sign, as a separate line, because a fee that appears only in the wallet is a fee the product hid.
const rentForTokenAccount = money.Base(2_039_280) // lamports, expressed for display

// networkFeeEstimate is the base fee plus a small priority allowance.
const networkFeeEstimate = money.Base(200)

var _ = ata.NewCreateInstruction
var _ = time.Now

// SignatureByMemo scans an account's recent transactions for the one carrying this memo.
//
// The window is deliberately shallow: a payment we are still waiting on is a recent one, and
// scanning deeper would cost more the longer the account has been in use — which is precisely
// backwards.
func (a *Adapter) SignatureByMemo(ctx context.Context, n network.Network,
	account, memo string) (chain.Confirmation, error) {
	if memo == "" {
		return chain.Confirmation{}, nil
	}
	key, err := solana.PublicKeyFromBase58(account)
	if err != nil {
		return chain.Confirmation{}, err
	}

	return rpc.Do(ctx, a.pool, n, func(c *solrpc.Client) (chain.Confirmation, error) {
		const window = 50
		sigs, err := c.GetSignaturesForAddressWithOpts(ctx, key,
			&solrpc.GetSignaturesForAddressOpts{
				Limit:      pointer(window),
				Commitment: solrpc.CommitmentConfirmed,
			})
		if err != nil {
			return chain.Confirmation{}, err
		}
		for _, s := range sigs {
			if s.Memo == nil || !strings.Contains(*s.Memo, memo) {
				continue
			}
			conf := chain.Confirmation{Found: true, Slot: int64(s.Slot)}
			if s.Err != nil {
				// Found, and it failed. That is a definite outcome, and a different one from
				// "not seen" — the budget goes back rather than being held indefinitely.
				conf.Err = fmt.Sprint(s.Err)
				return conf, nil
			}
			conf.Confirmed = s.ConfirmationStatus == solrpc.ConfirmationStatusConfirmed ||
				s.ConfirmationStatus == solrpc.ConfirmationStatusFinalized
			conf.Signature = s.Signature.String()
			return conf, nil
		}
		// Not in the window. NOT a failure — the payment stays `unknown` and is checked again.
		return chain.Confirmation{Found: false}, nil
	})
}

func pointer[T any](v T) *T { return &v }
