// Package policy is the eight safety rules, as pure functions.
//
// It performs no I/O, opens no connection, and reads no clock — `now` is a parameter. That is what
// makes the golden vectors possible, and the golden vectors are the most valuable tests in the
// repository: every boundary in this file is pinned by a JSON case that runs in microseconds.
//
// Nothing above this package may re-implement a rule that lives here. If the signer needs to know
// whether an amount fits, it calls the function.
package policy

import (
	"github.com/stone7890/leash/internal/domain/fault"
	"github.com/stone7890/leash/internal/domain/tier"
)

// Rule is one of the eight checks. The order of these constants IS the evaluation order, and the
// evaluation order is part of the contract — see Evaluate.
type Rule int

const (
	S0 Rule = iota // the networks match
	S1             // the allowance is alive
	S2             // the endpoint is allowed
	S3             // the amount fits one payment
	S4             // the amount fits the velocity window
	S5             // the amount fits the remaining budget
	S6             // the terms are ones we support
	S7             // the agent has not been stopped
)

// Rules is every rule, in evaluation order.
var Rules = []Rule{S0, S1, S2, S3, S4, S5, S6, S7}

func (r Rule) String() string {
	switch r {
	case S0:
		return "S0"
	case S1:
		return "S1"
	case S2:
		return "S2"
	case S3:
		return "S3"
	case S4:
		return "S4"
	case S5:
		return "S5"
	case S6:
		return "S6"
	case S7:
		return "S7"
	}
	return "S?"
}

// Fault is the stable wire code this rule blocks with. The agent SDK switches on these, so the
// mapping lives in exactly one place and is asserted against contracts/error-codes.json.
func (r Rule) Fault() *fault.E {
	switch r {
	case S0:
		return fault.NetworkMismatch
	case S1:
		return fault.AllowanceInactive
	case S2:
		return fault.EndpointNotAllowed
	case S3:
		return fault.PerTxLimit
	case S4:
		return fault.VelocityLimit
	case S5:
		return fault.BudgetExhausted
	case S6:
		return fault.UnsupportedTerms
	case S7:
		return fault.Killed
	}
	panic("policy: a rule without a fault — every rule must have a stable code")
}

// Code is the rule's wire code.
func (r Rule) Code() string { return r.Fault().Code() }

// Name is the label the interface shows in the timeline's phase 2.
func (r Rule) Name() string {
	switch r {
	case S0:
		return "network"
	case S1:
		return "allowance"
	case S2:
		return "allow-list"
	case S3:
		return "per-payment"
	case S4:
		return "velocity"
	case S5:
		return "budget"
	case S6:
		return "mint"
	case S7:
		return "kill switch"
	}
	return "?"
}

// CanBeDisabled reports whether an owner may turn this rule off.
//
// S0, S1, S5 and S6 have no toggle anywhere in the interface or the API: you cannot cross
// networks, you cannot spend from a dead allowance, you cannot exceed the budget, and you cannot
// pay in the wrong token. A product that let a customer disable those would not be a spend-control
// product.
func (r Rule) CanBeDisabled() bool {
	switch r {
	case S0, S1, S5, S6:
		return false
	}
	return true
}

// Tier says which layer actually enforced this rule, for invariant I3.
//
// It takes the allowance's stored expiry tier because S1 covers both revocation, which the chain
// enforces, and expiry, which under the shipping SPL adapter it does not. Reporting a single
// constant here would make the interface claim something the adapter had stopped doing.
func (r Rule) Tier(expiryTier tier.Tier, failedOnExpiry bool) tier.Tier {
	switch r {
	case S1:
		if failedOnExpiry {
			return expiryTier
		}
		return tier.OnChain
	case S5:
		// The cap is enforced by the chain. The signer checks it first so a doomed payment is
		// refused before a signature exists, but the binding constraint is on-chain.
		return tier.OnChain
	}
	return tier.Signer
}
