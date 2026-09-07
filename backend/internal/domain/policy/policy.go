package policy

import (
	"strings"
	"time"

	"github.com/stone7890/leash/internal/domain/fault"
	"github.com/stone7890/leash/internal/domain/ids"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/domain/state"
	"github.com/stone7890/leash/internal/domain/tier"
)

// MaxSnapshotAge is how stale the signer's cached view may be before S1 fails closed.
//
// The specification says the allowance may be cached for at most 60 seconds. Anything longer would
// make the number indefensible: the interface tells an owner their control is live, and a cache
// older than a minute is not. Kill, revoke and rotation do not wait for this — they are evicted by
// a change stream in milliseconds — so this is a floor, not the normal path.
const MaxSnapshotAge = 60 * time.Second

// AgentView is what the engine needs to know about the agent.
type AgentView struct {
	ID      ids.ID
	Network network.Network
	Killed  bool
}

// PolicyView is the soft-tier configuration an owner controls.
type PolicyView struct {
	AllowHosts []string
	// AllowPayTo optionally pins the recipients an agent may pay. Empty means any recipient at an
	// allowed host, which is the default. It exists because checking only the host is not enough:
	// an allowed endpoint that starts asking for payment to a different address is exactly the
	// case a host check waves through.
	AllowPayTo     []string
	AllowAll       bool
	PerTxMax       money.Base
	VelocityMax    money.Base
	VelocityWindow time.Duration
}

// VelocityEntry is one payment inside the rolling window.
type VelocityEntry struct {
	At     time.Time
	Amount money.Base
}

// AllowanceView is the cached on-chain state.
type AllowanceView struct {
	ID           ids.ID
	State        state.Allowance
	Cap          money.Base
	Drawn        money.Base
	Reserved     money.Base
	ExpiryTS     time.Time // zero means no expiry
	ExpiryTier   tier.Tier // stored, because the adapter decides it — I3
	Mint         string
	LastReadSlot int64
	Velocity     []VelocityEntry
}

// Snapshot is everything the engine is given. Nothing here permits I/O.
type Snapshot struct {
	Agent     AgentView
	Policy    PolicyView
	Allowance AllowanceView

	// KeyNetwork is the network the presented API key claims, read from its prefix. S0 compares it
	// against the agent's own network, which is the first thing checked and cannot be disabled.
	KeyNetwork network.Network

	// ExpectedMint is the USDC mint for the agent's network, from configuration. Passed in rather
	// than looked up, so this package stays free of configuration.
	ExpectedMint string

	// TakenAt is when this snapshot was read. S1 fails closed if it is too old.
	TakenAt time.Time
}

// Request is the challenge, canonicalised.
type Request struct {
	Host    string
	PayTo   string
	Amount  money.Base
	Mint    string
	Scheme  string
	Network network.Network

	// RecipientAccountExists is checked by the adapter at build time. Under the x402 `exact`
	// scheme, creating the recipient's token account is the endpoint's responsibility, not ours —
	// so a missing one is refused with an explanation the developer on the other end can act on.
	RecipientAccountExists bool
}

// Outcome is a single rule's result, as recorded in the append-only sign request and rendered by
// the interface as phase 2 of the timeline.
type Outcome string

const (
	Pass       Outcome = "pass"
	Fail       Outcome = "fail"
	NotReached Outcome = "not_reached"
)

// Check is one rule's line in the record.
type Check struct {
	Rule   string    `json:"rule"`
	Name   string    `json:"name"`
	Tier   tier.Tier `json:"tier"`
	Result Outcome   `json:"result"`
	Detail string    `json:"detail,omitempty"`
}

// Result is the verdict, and the evidence for it.
//
// Checks always has one entry per rule, in evaluation order, so the interface can render "N of N"
// from its length rather than from a literal. That resolves the contradiction between the
// prototype's "6 of 6", the deck's "7/7", and the eight rules that actually exist.
type Result struct {
	Verdict    state.Verdict
	FailedRule Rule
	Fault      *fault.E
	Checks     []Check
	// Warnings carries "wildcard" when the agent allows every endpoint. The interface flags such
	// an agent permanently, which is the chosen trade between flexibility and honesty.
	Warnings []string
}

func (r Result) Allowed() bool { return r.Verdict == state.VerdictAllowed }

// Evaluate runs S0 through S7 in order and stops at the first violation.
//
// The order is part of the contract, not an implementation detail. An agent that receives KILLED
// should stop; one that receives ENDPOINT_NOT_ALLOWED may try a different endpoint. If a killed
// agent with a bad host sometimes got one code and sometimes the other, the SDK's behaviour would
// be nondeterministic. S0 is first because a network mismatch means every subsequent comparison
// would be made against the wrong world.
//
// `now` is a parameter. This package never reads a clock.
func Evaluate(s Snapshot, r Request, now time.Time) Result {
	res := Result{Verdict: state.VerdictAllowed, FailedRule: -1}
	checks := make([]Check, 0, len(Rules))

	for i, rule := range Rules {
		ok, detail, failedOnExpiry := check(rule, s, r, now)
		t := rule.Tier(s.Allowance.ExpiryTier, failedOnExpiry)

		if ok {
			checks = append(checks, Check{
				Rule: rule.String(), Name: rule.Name(), Tier: t, Result: Pass, Detail: detail,
			})
			continue
		}

		checks = append(checks, Check{
			Rule: rule.String(), Name: rule.Name(), Tier: t, Result: Fail, Detail: detail,
		})
		// Everything after the first violation was not reached. The interface says so — "5 other
		// checks not reached" — rather than showing them as passes, which would imply they were
		// evaluated, or failures, which would be false.
		for _, later := range Rules[i+1:] {
			checks = append(checks, Check{
				Rule:   later.String(),
				Name:   later.Name(),
				Tier:   later.Tier(s.Allowance.ExpiryTier, false),
				Result: NotReached,
			})
		}
		res.Verdict = state.VerdictBlocked
		res.FailedRule = rule
		res.Fault = rule.Fault().WithMessage(blockMessage(rule, detail))
		res.Checks = checks
		res.Warnings = warnings(s)
		return res
	}

	res.Checks = checks
	res.Warnings = warnings(s)
	return res
}

func warnings(s Snapshot) []string {
	if s.Policy.AllowAll {
		return []string{"wildcard"}
	}
	return nil
}

func blockMessage(r Rule, detail string) string {
	if detail == "" {
		return r.Fault().Error()
	}
	return detail
}

// check returns whether the rule passed, a human detail, and whether an S1 failure was caused by
// expiry specifically — which decides the tier reported for it (I3).
func check(rule Rule, s Snapshot, r Request, now time.Time) (bool, string, bool) {
	switch rule {

	// S0 · the networks match.
	//
	// Three things must agree: the key's prefix, the agent's own network, and the network named in
	// the challenge. Checking only the prefix is the half-implemented version of this rule, and it
	// is exactly what lets a correctly-prefixed key spend against the other network's mint.
	case S0:
		if s.KeyNetwork != s.Agent.Network {
			return false, "the API key belongs to " + s.KeyNetwork.String() +
				" but the agent belongs to " + s.Agent.Network.String(), false
		}
		if r.Network != s.Agent.Network {
			return false, "the challenge is for " + r.Network.String() +
				" but the agent belongs to " + s.Agent.Network.String(), false
		}
		return true, "", false

	// S1 · the allowance is alive.
	case S1:
		if s.TakenAt.IsZero() || now.Sub(s.TakenAt) > MaxSnapshotAge {
			// Fail closed. A stale view of an allowance is not evidence that it is live.
			return false, "the allowance could not be confirmed as live", false
		}
		switch s.Allowance.State {
		case state.AllowanceActive:
			// fine
		case state.AllowanceRevoked:
			return false, "this allowance has been revoked", false
		case state.AllowancePending:
			return false, "this allowance is not yet confirmed on-chain", false
		default:
			return false, "this allowance is no longer active", false
		}
		// Expiry exactly at `now` is expired. `>=`, not `>`: an allowance that expires at 14:00
		// does not cover a payment at 14:00:00.000.
		if !s.Allowance.ExpiryTS.IsZero() && !now.Before(s.Allowance.ExpiryTS) {
			return false, "this allowance expired at " + s.Allowance.ExpiryTS.UTC().Format(time.RFC3339), true
		}
		return true, "", false

	// S2 · the endpoint is allowed.
	case S2:
		if !s.Policy.AllowAll {
			if !hostAllowed(s.Policy.AllowHosts, r.Host) {
				return false, r.Host + " is not on this agent's allow list", false
			}
		}
		// The recipient check is separate on purpose. An allowed endpoint that begins asking for
		// payment to a different address is precisely what a host-only check waves through.
		if len(s.Policy.AllowPayTo) > 0 && !contains(s.Policy.AllowPayTo, r.PayTo) {
			return false, "the recipient " + short(r.PayTo) + " is not one this agent may pay", false
		}
		return true, "", false

	// S3 · the amount fits one payment. Exactly the maximum is allowed.
	case S3:
		if s.Policy.PerTxMax > 0 && r.Amount > s.Policy.PerTxMax {
			return false, "$" + r.Amount.String() + " exceeds the per-payment maximum of $" +
				s.Policy.PerTxMax.String(), false
		}
		return true, "", false

	// S4 · the amount fits the velocity window.
	//
	// The engine evaluates this so a doomed payment is refused before any work is done, and so the
	// rule can be named precisely. The BINDING decision is made by the database, in the same
	// atomic operation that reserves the budget — an in-memory window would be correct for one
	// signer process and silently wrong the day there are two.
	case S4:
		if s.Policy.VelocityMax <= 0 || s.Policy.VelocityWindow <= 0 {
			return true, "", false
		}
		spent := VelocitySpent(s.Allowance.Velocity, now, s.Policy.VelocityWindow)
		if spent+r.Amount > s.Policy.VelocityMax {
			return false, "$" + (spent + r.Amount).String() + " would exceed the $" +
				s.Policy.VelocityMax.String() + " limit for the last " +
				s.Policy.VelocityWindow.String(), false
		}
		return true, "", false

	// S5 · the amount fits the remaining budget. Exactly the remainder is allowed.
	case S5:
		remaining := money.Remaining(s.Allowance.Cap, s.Allowance.Drawn, s.Allowance.Reserved)
		if r.Amount > remaining {
			return false, "$" + remaining.String() + " remains, and this payment is $" +
				r.Amount.String(), false
		}
		return true, "", false

	// S6 · the terms are ones we support.
	case S6:
		if r.Scheme != "exact" {
			return false, "only the `exact` scheme is supported, and this challenge asked for `" +
				r.Scheme + "`", false
		}
		if s.ExpectedMint == "" || r.Mint != s.ExpectedMint {
			return false, "the challenge asks for a token that is not " +
				s.Agent.Network.String() + " USDC", false
		}
		if !r.RecipientAccountExists {
			// Under `exact`, creating this account is the endpoint's or the facilitator's job.
			// Saying so is what makes the message actionable by whoever can fix it.
			return false, "the recipient has no token account for this mint — under the `exact` " +
				"scheme the endpoint or its facilitator must create it", false
		}
		return true, "", false

	// S7 · the agent has not been stopped.
	case S7:
		if s.Agent.Killed {
			return false, "this agent has been stopped", false
		}
		return true, "", false
	}

	panic("policy: an unevaluated rule — every rule in Rules must be handled")
}

// VelocitySpent sums the entries inside the window.
//
// An entry exactly at the window's edge is OUTSIDE it. The window is the last `window` of time up
// to but not including `now-window`, and this boundary is pinned by a golden vector — and by a
// second test against the real database, because the aggregation that makes the binding decision
// must agree with this function. If they ever disagree, the database is right and this is the bug.
func VelocitySpent(entries []VelocityEntry, now time.Time, window time.Duration) money.Base {
	cutoff := now.Add(-window)
	var sum money.Base
	for _, e := range entries {
		if e.At.After(cutoff) {
			sum += e.Amount
		}
	}
	return sum
}

// hostAllowed matches exactly, and only exactly.
//
// A subdomain of an allowed host is NOT allowed. That is the one-host rule of the recovery flow
// working from the other end: the one-tap button adds the exact host from the challenge, so
// matching loosely here would quietly widen every allow-list that button has ever touched.
func hostAllowed(allowed []string, host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	for _, a := range allowed {
		if strings.EqualFold(strings.TrimSpace(a), h) {
			return true
		}
	}
	return false
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func short(s string) string {
	if len(s) <= 11 {
		return s
	}
	return s[:4] + "…" + s[len(s)-4:]
}
