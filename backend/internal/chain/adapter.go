// Package chain is the only package that talks to Solana.
//
// Leash writes NO smart contract. It calls programs that already exist — the SPL Token program —
// through one interface, isolated here, so that when an assumption about somebody else's program
// turns out to be wrong, only this package changes.
//
// The specification's chapter 13 names two implementations. `spl` uses the standard
// approve/revoke delegate and is what ships. `subs` would use Solana's Subscriptions & Allowances
// primitive, and is stubbed until its real account layout is confirmed — the specification's own
// open question [Q1].
package chain

import (
	"context"
	"time"

	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/domain/state"
	"github.com/stone7890/leash/internal/domain/tier"
)

// UnsignedTx is a transaction built for the OWNER's wallet to sign. Leash never signs one:
// invariant I1, and there is no code path here that could.
type UnsignedTx struct {
	// Base64 is what the browser hands to the wallet adapter.
	Base64 string
	// Blockhash is pinned so the interface can tell how long it has. A Solana blockhash lasts
	// 60–90 seconds, and a person can be interrupted between seeing a prompt and signing it.
	Blockhash string
	ExpiresAt time.Time
	Summary   Summary
}

// Summary is what the interface shows before the wallet opens.
//
// Rent has its own line because under the per-agent account design the owner pays it for each
// agent, and a fee that appears only in the wallet is a fee the product hid.
type Summary struct {
	Cap        money.Base
	ExpiryTS   time.Time
	DelegateTo string
	NetworkFee money.Base
	Rent       money.Base
	// FundsMove is true when the transaction moves the owner's USDC into a per-agent account they
	// still own. The onboarding copy must say so — "funds stay put" would be false.
	FundsMove bool
}

// SignedTx is a draw, signed by the agent key inside the signer. It is the only signed thing this
// package produces.
type SignedTx struct {
	Base64    string
	Signature string
	// Memo is the nonce that went into the transaction.
	//
	// It matters because of who pays. When a facilitator sponsors the fee, the transaction's
	// on-chain identity is the FEE PAYER's signature — the first one — and that does not exist at
	// the moment we sign. So the signature we produce is not the one the chain will know it by,
	// and looking a payment up by it would never find anything.
	//
	// The memo is the protocol's own replay nonce and it is unique per payment, so it is what the
	// read-back matches on instead. Solana returns it on getSignaturesForAddress, which means the
	// indexer can find a sponsored payment without the agent having to report anything.
	Memo string
}

// AllowanceState is what a read-back found, and the slot it was found at.
//
// Every value carries its slot. Invariant I2 at its narrowest: a balance without a slot is a
// balance we cannot justify, and the schema refuses an active allowance that has none.
type AllowanceState struct {
	Cap      money.Base
	Drawn    money.Base
	ExpiryTS time.Time
	State    state.Allowance
	Slot     int64
	Exists   bool
}

// Capabilities is what an implementation ACTUALLY enforces on-chain.
//
// This is invariant I3's mechanism. The tier reported here is written to the allowance when it is
// created, and the interface renders from that stored value — never from a constant. A hardcoded
// table in the frontend would become a lie the moment the adapter changed, and the adapter is
// expected to change.
type Capabilities struct {
	// Cap is always true: a delegated amount is what the SPL program enforces.
	Cap bool
	// Revoke is always true.
	Revoke bool
	// Expiry is FALSE for the SPL delegate, which has no concept of one. Expiry is therefore a
	// signer-tier rule today, and the Rules tab shows it in the dashed card.
	Expiry bool
	// OneDelegatePerAccount records the constraint that forces a token account per agent.
	OneDelegatePerAccount bool
}

// ExpiryTier maps the capability onto the tier the interface must display.
func (c Capabilities) ExpiryTier() tier.Tier {
	if c.Expiry {
		return tier.OnChain
	}
	return tier.Signer
}

type CreateParams struct {
	Owner    string
	Agent    string
	Mint     string
	Program  string
	Cap      money.Base
	ExpiryTS time.Time
	Network  network.Network
}

type ModifyParams struct {
	Owner        string
	Agent        string
	AllowanceAcc string
	Mint         string
	Program      string
	NewCap       money.Base
	NewExpiryTS  time.Time
	Network      network.Network
}

type RevokeParams struct {
	Owner        string
	AllowanceAcc string
	Mint         string
	Program      string
	Network      network.Network
	// SweepTo is where the remaining balance goes. Revoking without sweeping leaves the owner's
	// money in an account they will not think to look at, so the revoke transaction carries both
	// instructions — and the public self-serve guide shows both.
	SweepTo string
}

type DrawParams struct {
	AllowanceAcc string
	AgentPubkey  string
	AgentSign    Signer
	PayTo        string
	Mint         string
	Program      string
	Amount       money.Base
	Blockhash    string
	Network      network.Network

	// Decimals of the mint. `transferChecked` carries them so the chain itself refuses a transfer
	// whose amount was computed against the wrong precision — which is why x402 requires it over
	// plain `transfer`.
	Decimals uint8

	// FeePayer is the FACILITATOR, from the offer's `extra.feePayer`.
	//
	// It is not the agent, and pay-kit's verifier refuses a transaction whose fee payer is also
	// the transfer authority — see docs/security/fee-payer-drain.md in their repository. So the
	// agent signs as authority only, the transaction goes out PARTIALLY signed, and the
	// facilitator adds its own signature when it settles.
	//
	// Empty means no sponsor was offered, and the agent must pay its own fee. That is legal but
	// unusual, and it is why the field is optional rather than required.
	FeePayer string

	// Memo is the optional server-pinned memo from `extra.memo`, capped at 256 bytes.
	Memo string
}

// Signer signs a message with an agent key. The key itself never leaves the signer process, so
// the adapter is handed the ability to sign rather than the key.
type Signer interface {
	PublicKey() string
	Sign(message []byte) ([]byte, error)
}

// Adapter is the whole of Leash's on-chain surface.
type Adapter interface {
	Capabilities() Capabilities

	// Owner-signed. Leash builds; the wallet signs.
	BuildCreate(ctx context.Context, p CreateParams) (UnsignedTx, error)
	BuildModify(ctx context.Context, p ModifyParams) (UnsignedTx, error)
	BuildRevoke(ctx context.Context, p RevokeParams) (UnsignedTx, error)

	// Signed by the agent key, inside the signer. The ONLY method that produces a signature.
	BuildDraw(ctx context.Context, p DrawParams) (SignedTx, error)

	// Read paths. No signature, and no writes.
	ReadAllowance(ctx context.Context, n network.Network, account string) (AllowanceState, error)
	AllowanceAccount(ctx context.Context, owner, agent, mint string, n network.Network) (string, error)
	Slot(ctx context.Context, n network.Network) (int64, error)
	Blockhash(ctx context.Context, n network.Network) (string, error)
	SignatureStatus(ctx context.Context, n network.Network, signature string) (Confirmation, error)
	SignatureByMemo(ctx context.Context, n network.Network, account, memo string) (Confirmation, error)
	AccountExists(ctx context.Context, n network.Network, account string) (bool, error)
}

// Confirmation is what a read-back by signature found.
//
// `Found` false does not mean failed. It means we have not seen it, which is the `unknown` state —
// a first-class outcome that holds its money and is re-checked, because assuming failure and
// freeing the budget is how one challenge gets paid twice.
type Confirmation struct {
	Found     bool
	Confirmed bool
	Slot      int64
	Err       string
	// Signature is the one the chain actually settled under. For a sponsored payment it is the
	// fee payer's, which we could not have known when we signed.
	Signature string
}

// SignatureByMemo finds a settled transaction by the memo it carried.
//
// This exists because of sponsored payments. When a facilitator pays the fee, the transaction's
// on-chain identity is the fee payer's signature — the first one — and that signature does not
// exist at the moment we sign. Looking a payment up by the signature we DID produce finds nothing,
// for ever, and the payment would sit in `unknown` until the sweep gave up on it after a day.
//
// The memo is the protocol's own replay nonce, unique per payment, and Solana returns it on
// getSignaturesForAddress. So the indexer can find a sponsored payment by scanning the account it
// was drawn from — without the agent having to report anything back, which matters because an
// agent using a stock x402 client has no idea Leash exists.
type MemoFinder interface {
	SignatureByMemo(ctx context.Context, n network.Network, account, memo string) (Confirmation, error)
}
