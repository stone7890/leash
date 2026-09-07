package spl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/gagliardetto/solana-go"
	computebudget "github.com/gagliardetto/solana-go/programs/compute-budget"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/programs/token"
	solrpc "github.com/gagliardetto/solana-go/rpc"
	"github.com/stone7890/leash/internal/chain"
	"github.com/stone7890/leash/internal/chain/rpc"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
)

// blockhashWindow is how long an unsigned transaction is offered for.
//
// A Solana blockhash lasts 60–90 seconds, and a person can be interrupted between seeing a wallet
// prompt and signing it. The interface rebuilds rather than letting a stale transaction be signed
// and fail — which is what the prototype's "Try again" branch is for.
const blockhashWindow = 45 * time.Second

// tokenAccountSize is the fixed on-chain size of an SPL token account: 165 bytes. It is a constant
// of the program, not a thing to query, and it decides the rent exemption the owner pays per agent.
const tokenAccountSize = uint64(165)

// The agent needs NO SOL of its own.
//
// An earlier version of this file funded each agent with a small float so it could pay its own
// network fees, on the reading that the specification left the question open ([Q2]). Reading
// pay-kit's spec settled it in the other direction: the x402 `exact` challenge carries
// `extra.feePayer`, the facilitator sponsors the fee, and pay-kit's verifier REFUSES a transaction
// whose fee payer is also the transfer authority.
//
// So an agent paying its own fees is not merely unnecessary — it is a shape the protocol rejects.
// The float is gone, and with it a per-agent cost the owner was being asked to cover for nothing.

// BuildCreate is the delegation, and it is THREE owner-signed instructions.
//
// The specification's picture is "one transaction creates the allowance; funds stay put". Under
// the SPL delegate that is not achievable, because approve permits one delegate per token account
// — so each agent needs its own account, and the owner's USDC moves into it.
//
// The account is still owned by the owner. They can drain it at any moment, and Leash never holds
// a key to it, so invariant I1 is untouched. But the money moves, and the onboarding copy says so.
func (a *Adapter) BuildCreate(ctx context.Context, p chain.CreateParams) (chain.UnsignedTx, error) {
	owner, err := solana.PublicKeyFromBase58(p.Owner)
	if err != nil {
		return chain.UnsignedTx{}, fmt.Errorf("owner address: %w", err)
	}
	agent, err := solana.PublicKeyFromBase58(p.Agent)
	if err != nil {
		return chain.UnsignedTx{}, fmt.Errorf("agent address: %w", err)
	}
	mint, err := solana.PublicKeyFromBase58(p.Mint)
	if err != nil {
		return chain.UnsignedTx{}, fmt.Errorf("mint address: %w", err)
	}

	allowanceAddr, err := a.AllowanceAccount(ctx, p.Owner, p.Agent, p.Mint, p.Network)
	if err != nil {
		return chain.UnsignedTx{}, err
	}
	allowance := solana.MustPublicKeyFromBase58(allowanceAddr)

	ownerATA, _, err := solana.FindAssociatedTokenAddress(owner, mint)
	if err != nil {
		return chain.UnsignedTx{}, fmt.Errorf("owner token account: %w", err)
	}

	rentLamports, err := rpc.Do(ctx, a.pool, p.Network, func(c *solrpc.Client) (uint64, error) {
		return c.GetMinimumBalanceForRentExemption(ctx, tokenAccountSize, solrpc.CommitmentConfirmed)
	})
	if err != nil {
		return chain.UnsignedTx{}, fmt.Errorf("rent exemption: %w", err)
	}

	instructions := []solana.Instruction{
		// 1 · the agent's own token account, paid for and owned by the owner.
		//
		//     WithSeed, not a plain CreateAccount: a plain one requires the NEW account to sign,
		//     which would mean Leash holding a second key just to create it. With a seed, only the
		//     owner signs, and the address is still deterministic.
		system.NewCreateAccountWithSeedInstruction(
			owner, AllowanceSeed(p.Agent, p.Mint), rentLamports, tokenAccountSize,
			solana.TokenProgramID, owner, allowance, owner,
		).Build(),
		token.NewInitializeAccountInstruction(allowance, mint, owner, solana.SysVarRentPubkey).Build(),

		// 2 · move the cap into it. THIS is the step the copy must describe honestly.
		token.NewTransferInstruction(uint64(p.Cap), ownerATA, allowance, owner, nil).Build(),

		// 3 · the agent may draw up to the cap, and not one unit more. Solana enforces this with
		//     or without Leash running, which is what makes the cap a hard-tier rule.
		token.NewApproveInstruction(uint64(p.Cap), allowance, agent, owner, nil).Build(),
	}

	return a.assemble(ctx, p.Network, owner, instructions, chain.Summary{
		Cap:        p.Cap,
		ExpiryTS:   p.ExpiryTS,
		DelegateTo: p.Agent,
		NetworkFee: networkFeeEstimate,
		Rent:       money.Base(rentLamports),
		FundsMove:  true,
	})
}

// BuildModify is the top-up and the extension.
//
// `approve` REPLACES the delegated amount rather than adding to it, so raising a cap means moving
// the difference in and approving the new total. The allowance's epoch increments, and the drawn
// counter resets with it — without that, the amount already spent would count against the new cap.
//
// The old delegation keeps working until this confirms. There is no dead window, and the interface
// must not show one.
func (a *Adapter) BuildModify(ctx context.Context, p chain.ModifyParams) (chain.UnsignedTx, error) {
	owner, err := solana.PublicKeyFromBase58(p.Owner)
	if err != nil {
		return chain.UnsignedTx{}, err
	}
	agent, err := solana.PublicKeyFromBase58(p.Agent)
	if err != nil {
		return chain.UnsignedTx{}, err
	}
	mint, err := solana.PublicKeyFromBase58(p.Mint)
	if err != nil {
		return chain.UnsignedTx{}, err
	}
	allowance, err := solana.PublicKeyFromBase58(p.AllowanceAcc)
	if err != nil {
		return chain.UnsignedTx{}, err
	}
	ownerATA, _, err := solana.FindAssociatedTokenAddress(owner, mint)
	if err != nil {
		return chain.UnsignedTx{}, err
	}

	// What is already in the account counts towards the new cap; only the shortfall moves.
	held, err := a.tokenBalance(ctx, p.Network, allowance)
	if err != nil {
		return chain.UnsignedTx{}, err
	}
	var instructions []solana.Instruction
	if p.NewCap > held {
		instructions = append(instructions, token.NewTransferInstruction(
			uint64(p.NewCap-held), ownerATA, allowance, owner, nil).Build())
	}
	instructions = append(instructions, token.NewApproveInstruction(
		uint64(p.NewCap), allowance, agent, owner, nil).Build())

	return a.assemble(ctx, p.Network, owner, instructions, chain.Summary{
		Cap:        p.NewCap,
		ExpiryTS:   p.NewExpiryTS,
		DelegateTo: p.Agent,
		NetworkFee: networkFeeEstimate,
		FundsMove:  p.NewCap > held,
	})
}

// BuildRevoke is TWO instructions: revoke, and sweep.
//
// Revoking alone stops the agent but leaves the remaining balance sitting in the per-agent
// account. That is the owner's money, in an account they will not think to look at. So the sweep
// travels with the revoke — and the public self-serve guide shows both, because an owner following
// it during our outage must not strand their own funds.
func (a *Adapter) BuildRevoke(ctx context.Context, p chain.RevokeParams) (chain.UnsignedTx, error) {
	owner, err := solana.PublicKeyFromBase58(p.Owner)
	if err != nil {
		return chain.UnsignedTx{}, err
	}
	allowance, err := solana.PublicKeyFromBase58(p.AllowanceAcc)
	if err != nil {
		return chain.UnsignedTx{}, err
	}
	sweepTo, err := solana.PublicKeyFromBase58(p.SweepTo)
	if err != nil {
		return chain.UnsignedTx{}, err
	}

	instructions := []solana.Instruction{
		// 1 · the agent can no longer draw, whatever it does and whoever holds its key.
		token.NewRevokeInstruction(allowance, owner, nil).Build(),
	}

	// 2 · and the remainder comes home.
	held, err := a.tokenBalance(ctx, p.Network, allowance)
	if err != nil {
		return chain.UnsignedTx{}, err
	}
	if held > 0 {
		instructions = append(instructions, token.NewTransferInstruction(
			uint64(held), allowance, sweepTo, owner, nil).Build())
	}

	return a.assemble(ctx, p.Network, owner, instructions, chain.Summary{
		DelegateTo: "",
		NetworkFee: networkFeeEstimate,
	})
}

// BuildDraw is the ONLY method that produces a signature, and the agent key makes it.
//
// THE INSTRUCTION LAYOUT IS NOT OURS TO CHOOSE. pay-kit's verifier reads it POSITIONALLY, and a
// transaction in any other shape is refused before it reaches the chain:
//
//	[0] ComputeBudget SetComputeUnitLimit
//	[1] ComputeBudget SetComputeUnitPrice   (<= 5,000,000 microlamports)
//	[2] transferChecked  ->  ATA(payTo, mint, tokenProgram)
//	[3] Memo             (exactly one, always)
//	    ... only Memo or Lighthouse may follow; total instructions in [3, 6]
//
// Three of those deserve saying out loud.
//
// The DESTINATION is the recipient's associated token account, not the recipient. Sending to the
// address in `payTo` directly produces a transaction their verifier rejects and a customer who
// cannot tell why.
//
// The MEMO is mandatory and is the replay protection. x402 on Solana has no nonce field: two
// otherwise identical payments — same amount, mint, recipient, blockhash — would be the same
// transaction, so the memo carries either the seller's pinned string or sixteen random bytes to
// keep them distinct on chain.
//
// The SOURCE is where we deliberately differ from pay-kit's own client, which derives it as
// ATA(payer, mint) — it assumes whoever signs owns the money. Ours is the owner's per-agent
// allowance account, with the agent as its DELEGATE, which is the entire product. Their verifier
// does not constrain the source, so a delegated transfer satisfies it; their builder simply cannot
// express one, which is why we build our own and test it against their vectors instead.
func (a *Adapter) BuildDraw(_ context.Context, p chain.DrawParams) (chain.SignedTx, error) {
	agent, err := solana.PublicKeyFromBase58(p.AgentPubkey)
	if err != nil {
		return chain.SignedTx{}, err
	}
	source, err := solana.PublicKeyFromBase58(p.AllowanceAcc)
	if err != nil {
		return chain.SignedTx{}, err
	}
	recipient, err := solana.PublicKeyFromBase58(p.PayTo)
	if err != nil {
		return chain.SignedTx{}, err
	}
	mint, err := solana.PublicKeyFromBase58(p.Mint)
	if err != nil {
		return chain.SignedTx{}, fmt.Errorf("mint: %w", err)
	}
	blockhash, err := solana.HashFromBase58(p.Blockhash)
	if err != nil {
		return chain.SignedTx{}, fmt.Errorf("blockhash: %w", err)
	}

	tokenProgram := solana.TokenProgramID
	if p.Program != "" {
		tokenProgram, err = solana.PublicKeyFromBase58(p.Program)
		if err != nil {
			return chain.SignedTx{}, fmt.Errorf("token program: %w", err)
		}
	}

	decimals := p.Decimals
	if decimals == 0 {
		// USDC is six. Zero would make transferChecked refuse the amount, so the default is the
		// only value that could be right rather than a guess at nothing.
		decimals = 6
	}

	// The recipient's ATA, not the recipient — derived against the mint's OWN token program, so a
	// Token-2022 mint lands at the right address rather than one that looks plausible and does not
	// exist.
	destination, err := associatedTokenAddress(recipient, mint, tokenProgram)
	if err != nil {
		return chain.SignedTx{}, fmt.Errorf("recipient token account: %w", err)
	}

	memo := p.Memo
	if memo == "" {
		// Sixteen random bytes, hex encoded. This is the replay protection the protocol has
		// instead of a nonce field.
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return chain.SignedTx{}, fmt.Errorf("memo nonce: %w", err)
		}
		memo = hex.EncodeToString(raw)
	}
	if len(memo) > maxMemoBytes {
		return chain.SignedTx{}, fmt.Errorf(
			"the offer pins a memo of %d bytes, over the x402 limit of %d", len(memo), maxMemoBytes)
	}

	instructions := []solana.Instruction{
		computeUnitLimit(defaultComputeUnitLimit),
		computeUnitPrice(defaultComputeUnitPrice),
		// The agent is the DELEGATE on the source account, so it may move exactly as much as the
		// owner approved — and the chain refuses anything more. That refusal is the hard tier.
		token.NewTransferCheckedInstruction(
			uint64(p.Amount), decimals, source, mint, destination, agent, nil,
		).Build(),
		memoInstruction(memo, agent),
	}

	// The facilitator pays. If the offer named none, the agent does — legal, but unsponsored.
	payer := agent
	if p.FeePayer != "" {
		fp, err := solana.PublicKeyFromBase58(p.FeePayer)
		if err != nil {
			return chain.SignedTx{}, fmt.Errorf("the offer's feePayer is not an address: %w", err)
		}
		if fp.Equals(agent) {
			// Refused here rather than by the facilitator, so the message names the real problem
			// instead of arriving as a verification failure.
			return chain.SignedTx{}, fmt.Errorf(
				"the offer names the agent as its own fee payer, which x402 refuses: the fee " +
					"payer must not be the transfer authority")
		}
		payer = fp
	}

	tx, err := solana.NewTransaction(instructions, blockhash, solana.TransactionPayer(payer))
	if err != nil {
		return chain.SignedTx{}, err
	}

	msg, err := tx.Message.MarshalBinary()
	if err != nil {
		return chain.SignedTx{}, err
	}
	sigBytes, err := p.AgentSign.Sign(msg)
	if err != nil {
		return chain.SignedTx{}, fmt.Errorf("signing: %w", err)
	}
	var agentSig solana.Signature
	copy(agentSig[:], sigBytes)

	// Signatures are POSITIONAL: one slot per required signer, in the message's own order. The
	// facilitator's slot is left zeroed for it to fill — that is what "partially signed" means on
	// Solana, and putting the agent's signature in the wrong slot produces a failure that looks
	// like a bad key.
	signers := tx.Message.AccountKeys[:tx.Message.Header.NumRequiredSignatures]
	tx.Signatures = make([]solana.Signature, len(signers))
	placed := false
	for i, k := range signers {
		if k.Equals(agent) {
			tx.Signatures[i] = agentSig
			placed = true
		}
	}
	if !placed {
		return chain.SignedTx{}, fmt.Errorf(
			"the agent is not a required signer of this transaction, so its signature has nowhere " +
				"to go")
	}

	encoded, err := tx.ToBase64()
	if err != nil {
		return chain.SignedTx{}, err
	}
	return chain.SignedTx{
		Base64: encoded, Signature: agentSig.String(), Memo: memo,
	}, nil
}

// The compute budget.
//
// The verifier reads instructions [0] and [1] BY INDEX and caps the PRICE at five million
// microlamports; it does not constrain the limit. So the price matches pay-kit's client exactly,
// and the limit is ours to set.
//
// pay-kit's client defaults to 20,000 units. That is too tight here, and the reason is worth
// recording rather than rediscovering: a delegated `transferChecked` costs about 6,300 units, and
// the mandatory Memo instruction then needs more than the 13,700 left — the transaction fails with
// "exceeded CUs meter" AFTER the payment instruction has already succeeded, which is the most
// confusing possible place to run out.
//
// 60,000 leaves room for the transfer, the memo, and a wallet's own guard instructions, and still
// costs almost nothing at a price of one microlamport.
const (
	defaultComputeUnitLimit = uint32(60_000)
	defaultComputeUnitPrice = uint64(1)
	maxMemoBytes            = 256
)

func computeUnitLimit(units uint32) solana.Instruction {
	return computebudget.NewSetComputeUnitLimitInstruction(units).Build()
}

func computeUnitPrice(micro uint64) solana.Instruction {
	return computebudget.NewSetComputeUnitPriceInstruction(micro).Build()
}

// associatedTokenAddress derives an ATA against a specific token program.
//
// solana-go's helper assumes the classic SPL program. USDC is classic today, but Token-2022 mints
// exist and derive to a different address — using the wrong program produces a valid-looking
// address that holds nothing, and a transfer that fails for a reason nobody can see.
func associatedTokenAddress(owner, mint, tokenProgram solana.PublicKey) (solana.PublicKey, error) {
	addr, _, err := solana.FindProgramAddress(
		[][]byte{owner.Bytes(), tokenProgram.Bytes(), mint.Bytes()},
		solana.SPLAssociatedTokenAccountProgramID,
	)
	return addr, err
}

// memoInstruction attaches the server-pinned memo, capped by the protocol at 256 bytes.
func memoInstruction(memo string, signer solana.PublicKey) solana.Instruction {
	if len(memo) > 256 {
		memo = memo[:256]
	}
	return solana.NewInstruction(
		solana.MustPublicKeyFromBase58("MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr"),
		solana.AccountMetaSlice{{PublicKey: signer, IsSigner: true, IsWritable: false}},
		[]byte(memo),
	)
}

// assemble builds an unsigned transaction for the owner's wallet and records when it expires.
func (a *Adapter) assemble(ctx context.Context, n network.Network, payer solana.PublicKey,
	instructions []solana.Instruction, summary chain.Summary) (chain.UnsignedTx, error) {

	blockhashStr, err := a.Blockhash(ctx, n)
	if err != nil {
		return chain.UnsignedTx{}, fmt.Errorf("recent blockhash: %w", err)
	}
	blockhash, err := solana.HashFromBase58(blockhashStr)
	if err != nil {
		return chain.UnsignedTx{}, err
	}

	tx, err := solana.NewTransaction(instructions, blockhash, solana.TransactionPayer(payer))
	if err != nil {
		return chain.UnsignedTx{}, err
	}
	encoded, err := tx.ToBase64()
	if err != nil {
		return chain.UnsignedTx{}, err
	}
	return chain.UnsignedTx{
		Base64:    encoded,
		Blockhash: blockhashStr,
		ExpiresAt: time.Now().UTC().Add(blockhashWindow),
		Summary:   summary,
	}, nil
}

func (a *Adapter) tokenBalance(ctx context.Context, n network.Network, account solana.PublicKey) (
	money.Base, error) {
	return rpc.Do(ctx, a.pool, n, func(c *solrpc.Client) (money.Base, error) {
		res, err := c.GetTokenAccountBalance(ctx, account, solrpc.CommitmentConfirmed)
		if err != nil {
			return 0, nil // no account yet is a zero balance, not a failure
		}
		var v uint64
		if _, err := fmt.Sscanf(res.Value.Amount, "%d", &v); err != nil {
			return 0, err
		}
		return money.Base(v), nil
	})
}
