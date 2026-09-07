package store

import (
	"time"

	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/domain/policy"
	"github.com/stone7890/leash/internal/domain/state"
	"github.com/stone7890/leash/internal/domain/tier"
)

// The shapes this package returns.
//
// They are plain structs of domain types and scalars. No driver type appears in any exported
// signature here — that is the rule `verify-architecture` checks, and it is the one that matters:
// an import rule passes happily while a `Coll(name) *mongo.Collection` helper exists, at which
// point the boundary is decorative.

type Org struct {
	ID            string
	OwnerWallet   string
	Plan          string
	ActiveNetwork network.Network
	WorkspaceName string
	CreatedAt     time.Time
}

type Agent struct {
	ID        string
	OrgID     string
	Name      string
	Template  string
	Network   network.Network
	RunsAs    string
	Pubkey    string
	Killed    bool
	KilledAt  time.Time
	CreatedAt time.Time
}

type Policy struct {
	AgentID        string
	AllowHosts     []string
	AllowPayTo     []string
	AllowAll       bool
	PerTxMax       money.Base
	VelocityMax    money.Base
	VelocityWindow time.Duration
	UpdatedAt      time.Time
}

type Allowance struct {
	ID           string
	AgentID      string
	Network      network.Network
	OnchainAddr  string
	Mint         string
	TokenProgram string
	Cap          money.Base
	Drawn        money.Base
	Reserved     money.Base
	LastReadSlot int64
	LastReadAt   time.Time
	ExpiryTS     time.Time
	ExpiryTier   tier.Tier
	State        state.Allowance
	StateAt      time.Time
	Epoch        int
	CreatedAt    time.Time
}

// Remaining is the budget arithmetic, in one place. cap − drawn − reserved.
func (a Allowance) Remaining() money.Base {
	return money.Remaining(a.Cap, a.Drawn, a.Reserved)
}

// Exhausted is DERIVED, never stored. The cap is fully drawn but the allowance is live until it
// expires, and the interface prompts a top-up rather than showing a dead agent.
func (a Allowance) Exhausted() bool {
	return a.State == state.AllowanceActive && a.Remaining() == 0
}

// ExpiringSoon is derived too: under 24 hours, amber in the Expires column.
func (a Allowance) ExpiringSoon(now time.Time) bool {
	if a.ExpiryTS.IsZero() || a.State != state.AllowanceActive {
		return false
	}
	return a.ExpiryTS.Sub(now) < 24*time.Hour && a.ExpiryTS.After(now)
}

type Payment struct {
	ID            string
	SignRequestID string
	AgentID       string
	AgentName     string
	AllowanceID   string
	OrgID         string
	Network       network.Network
	Signature     string
	// Memo is the transaction's replay nonce. It is how a SPONSORED payment is found on chain:
	// the fee payer's signature is the transaction's identity, and we never see it.
	Memo          string
	PayTo         string
	Host          string
	Amount        money.Base
	State         state.Payment
	StateAt       time.Time
	ConfirmedSlot int64
	LastCheckedAt time.Time
	CreatedAt     time.Time
}

type SignRequest struct {
	ID              string
	AgentID         string
	Network         network.Network
	ChallengeHash   string
	Verdict         state.Verdict
	FailedRule      string
	Checks          []policy.Check
	Requirements    map[string]any
	RequirementsRaw string
	Warnings        []string
	CreatedAt       time.Time
}

type HandshakeEvent struct {
	ID           string
	PaymentID    string
	Network      network.Network
	Phase        state.Phase
	Writer       state.Writer
	ReadBackSlot int64
	Detail       map[string]any
	TraceID      string
	At           time.Time
}

// Snapshot is what the signer's cache holds and the policy engine is given: one agent, its policy,
// and its live allowance, read in ONE round trip rather than three.
type Snapshot struct {
	Agent     Agent
	Policy    Policy
	Allowance Allowance
	// Velocity is the window as stored on the allowance document. It lives there so that one
	// atomic update can decide S4 and S5 together — an in-memory window would be correct for a
	// single signer process and silently wrong the day there are two.
	Velocity []policy.VelocityEntry
	TakenAt  time.Time
}

// Claim is the outcome of trying to claim a challenge (invariant I7).
type Claim struct {
	// Won is true when this caller is the one that must do the work.
	Won bool
	// Replay carries the stored answer when the challenge was already settled. A retry gets the
	// ORIGINAL response, byte for byte.
	Replay map[string]any
	// InFlight is true when another attempt holds the claim right now. The caller answers 409 with
	// retriable set, and the SDK must retry WITH THE SAME CHALLENGE — a fresh one would create a
	// second payment for one API call.
	InFlight  bool
	Blockhash string
}

func nowUTC() time.Time { return time.Now().UTC() }

func phaseOf(s string) state.Phase   { return state.Phase(s) }
func writerOf(s string) state.Writer { return state.Writer(s) }
