// Package tier is invariant I3 as a value.
//
// Some rules the chain enforces and nobody can bypass. Others the signer enforces, which means
// they hold exactly as long as the signer is the only route to a signature. The interface must
// always say which, and it must say it truthfully.
//
// The tier is DATA, not a constant. The shipping adapter is the SPL approve/revoke delegate, which
// has no expiry — so expiry is a signer-tier rule today and would become an on-chain one if the
// Subscriptions & Allowances primitive ships. A hardcoded table in the frontend would become a lie
// the moment the adapter changed, and the adapter is expected to change. So the tier is stored on
// the allowance and rendered from there.
package tier

type Tier string

const (
	// OnChain: Solana enforces it. It survives Leash being down entirely.
	OnChain Tier = "onchain"
	// Signer: checked before every signature. It does not survive the signer being bypassed —
	// but an unreachable signer signs nothing, so the failure direction is safe.
	Signer Tier = "signer"
)

func (t Tier) Valid() bool { return t == OnChain || t == Signer }

// Label is the heading the interface uses. Kept here rather than in the frontend so both runtimes
// say the same thing.
func (t Tier) Label() string {
	if t == OnChain {
		return "Enforced by Solana"
	}
	return "Enforced by Leash signer"
}

// SurvivesOutage answers the question a customer actually has: if Leash disappears, does this rule
// still bind?
func (t Tier) SurvivesOutage() bool { return t == OnChain }
