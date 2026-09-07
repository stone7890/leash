// Package state holds the four independent axes and their legal transitions.
//
// From docs/02-invariants.md: allowance state, payment state, verdict and network are INDEPENDENT
// and must never be merged into one enum. A blocked verdict produces no payment at all, so it has
// no payment state. An `unknown` payment has a perfectly healthy allowance. `exhausted` is derived
// rather than stored. Merging any two of them is a data-model bug, not a simplification.
package state

import "errors"

// Allowance is the on-chain object's lifecycle.
//
// `exhausted` and `expiring-soon` are DERIVED at query time from chain data and are never stored:
// storing them would create a second thing that can be stale.
type Allowance string

const (
	AllowancePending   Allowance = "pending"
	AllowanceActive    Allowance = "active"
	AllowanceExhausted Allowance = "exhausted"
	AllowanceExpired   Allowance = "expired"
	AllowanceRevoked   Allowance = "revoked"
)

// Payment is the lifecycle of one draw.
//
// `unknown` is a first-class state and is NOT a failure. It holds its money in the reservation and
// is re-checked for as long as it takes. Merging it into `failed` is the shortest path to paying
// twice for one challenge; merging it into `confirmed` is lying. That is invariant I4.
type Payment string

const (
	PaymentSigned    Payment = "signed"
	PaymentSubmitted Payment = "submitted"
	PaymentConfirmed Payment = "confirmed"
	PaymentFailed    Payment = "failed"
	PaymentUnknown   Payment = "unknown"
)

// Verdict is the outcome of evaluating S0–S7. Exactly one is produced per /v1/sign, and it is
// written to the append-only record whether it allowed or blocked.
type Verdict string

const (
	VerdictAllowed Verdict = "allowed"
	VerdictBlocked Verdict = "blocked"
)

// Phase is one of the six steps in a payment's handshake.
type Phase string

const (
	PhaseChallengeReceived Phase = "challenge_received"
	PhaseRulesEvaluated    Phase = "rules_evaluated"
	PhaseSigned            Phase = "signed"
	PhaseReplayed          Phase = "replayed"
	PhaseBroadcast         Phase = "broadcast"
	PhaseConfirmed         Phase = "confirmed"
)

// Writer records which component wrote a handshake phase.
//
// It is stored rather than inferred so that a violation of I4 is visible in the data. The schema
// refuses a `confirmed` row whose writer is not the indexer, or which carries no slot.
type Writer string

const (
	WriterSigner  Writer = "signer"
	WriterIndexer Writer = "indexer"
)

var (
	ErrUnknownState      = errors.New("unknown state")
	ErrIllegalTransition = errors.New("illegal state transition")
)

// PhaseOrder is the canonical display order of the timeline. A phase that has not happened is
// ABSENT, never a placeholder — the interface renders absence differently for a blocked payment
// ("not reached") and one still in flight (a spinner).
var PhaseOrder = []Phase{
	PhaseChallengeReceived, PhaseRulesEvaluated, PhaseSigned,
	PhaseReplayed, PhaseBroadcast, PhaseConfirmed,
}

// PhaseWriter says which component is permitted to write a phase, mirroring the database
// validator. Phase 4 has no writer: `replayed` happens inside the customer's agent runtime, which
// we do not run and cannot observe. Fabricating a timestamp for it would be inventing evidence in
// an audit trail.
func PhaseWriter(p Phase) (Writer, bool) {
	switch p {
	case PhaseChallengeReceived, PhaseRulesEvaluated, PhaseSigned:
		return WriterSigner, true
	case PhaseBroadcast, PhaseConfirmed:
		return WriterIndexer, true
	}
	return "", false
}

// allowanceNext is the legal transition table. `pending -> active` appears here, but the schema
// enforces the part that matters: an `active` allowance must carry the slot it was read at, so
// the transition cannot happen without a read-back (I4).
var allowanceNext = map[Allowance][]Allowance{
	AllowancePending: {AllowanceActive, AllowanceExpired, AllowanceRevoked},
	AllowanceActive:  {AllowanceExpired, AllowanceRevoked},
	// Terminal.
	AllowanceExhausted: {},
	AllowanceExpired:   {},
	AllowanceRevoked:   {},
}

// paymentNext encodes invariant I4: every exit from signed, submitted and unknown is a read-back.
// There is no transition that re-signs and none that infers an outcome from an HTTP response.
var paymentNext = map[Payment][]Payment{
	PaymentSigned:    {PaymentSubmitted, PaymentConfirmed, PaymentUnknown, PaymentFailed},
	PaymentSubmitted: {PaymentConfirmed, PaymentUnknown, PaymentFailed},
	PaymentUnknown:   {PaymentConfirmed, PaymentFailed},
	// Terminal.
	PaymentConfirmed: {},
	PaymentFailed:    {},
}

func (a Allowance) CanBecome(next Allowance) bool { return contains(allowanceNext[a], next) }
func (p Payment) CanBecome(next Payment) bool     { return contains(paymentNext[p], next) }

// Live reports whether an allowance can still spend. These are exactly the states covered by the
// `one_active_allowance` partial unique index, and the materialised key that backs it.
func (a Allowance) Live() bool { return a == AllowancePending || a == AllowanceActive }

// Terminal reports whether a payment has reached an outcome we will not revisit.
func (p Payment) Terminal() bool { return p == PaymentConfirmed || p == PaymentFailed }

// InFlight reports whether a payment still holds budget. `unknown` does, which is the whole point
// of I4: a payment whose outcome we have not observed has not released its money.
func (p Payment) InFlight() bool {
	return p == PaymentSigned || p == PaymentSubmitted || p == PaymentUnknown
}

func contains[T comparable](xs []T, x T) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (a Allowance) String() string { return string(a) }
func (p Payment) String() string   { return string(p) }
func (v Verdict) String() string   { return string(v) }
func (p Phase) String() string     { return string(p) }
func (w Writer) String() string    { return string(w) }
