// Package hot is THE SIGNING PATH, and it is its own package so the bans on it are machine-checkable.
//
// It may not directly import net/http, internal/chain/rpc, or internal/alerting. Its constructor
// takes exactly what it needs and nothing capable of an outbound call, so adding one means changing
// a signature that `verify-architecture` asserts. And a test replaces the default HTTP transport
// with one that panics, runs a thousand signatures, and requires that nothing panicked — which is
// what would actually catch a "quick" RPC call added later.
//
// The budget is under 50ms at p50 and under 300ms at p99, across THREE synchronous round trips:
// claim, admit, record. Everything else is served from process memory.
package hot

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/stone7890/leash/internal/chain"
	"github.com/stone7890/leash/internal/domain/challenge"
	"github.com/stone7890/leash/internal/domain/fault"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/domain/policy"
	"github.com/stone7890/leash/internal/domain/state"
	"github.com/stone7890/leash/internal/store"
)

// Store is the narrow slice of persistence the signing path needs. Declared here rather than
// imported wholesale, so the path cannot reach a repository it has no business touching.
type Store interface {
	ClaimChallenge(ctx context.Context, hash, agentID string, net network.Network,
		blockhash string, now time.Time) (store.Claim, error)
	SettleClaim(ctx context.Context, hash string, response map[string]any,
		signRequestID string, now time.Time) error
	AdmitDraw(ctx context.Context, net network.Network, allowanceID string, amount money.Base,
		velocityMax money.Base, window time.Duration, now time.Time) (store.Allowance, error)
	RecordVerdict(ctx context.Context, sr store.SignRequest, p *store.Payment,
		now time.Time) (string, string, error)
	SnapshotForKey(ctx context.Context, keyHash string, now time.Time) (store.Snapshot, error)
}

// Keys hands out the ability to sign without handing out the key.
type Keys interface {
	For(ctx context.Context, agentID string) (ed25519.PrivateKey, error)
}

// Blockhashes is the per-network cache, refreshed by a goroutine outside this package. The signing
// path reads a value; it never calls an RPC.
type Blockhashes interface {
	Get(n network.Network) (string, bool)
}

// Snapshots is the cached agent/policy/allowance view, evicted by a change stream on kill.
type Snapshots interface {
	Get(ctx context.Context, keyHash string, now time.Time) (store.Snapshot, error)
	Evict(agentID string)
}

// Timeline receives handshake phases 1–3. It is a queue, drained elsewhere: writing the timeline
// synchronously would put an audit write on the critical path of a payment.
type Timeline interface {
	Enqueue(store.HandshakeEvent)
}

// Signer is the whole of the hot path.
//
// Note what this struct CANNOT do. There is no HTTP client, no RPC pool, no alerting sink. The
// only outbound network call reachable from here is the KMS unwrap behind Keys, which is in the
// budget at 20ms and 80ms and is cached for ten minutes.
type Signer struct {
	store     Store
	keys      Keys
	snapshots Snapshots
	blocks    Blockhashes
	timeline  Timeline
	adapter   chain.Adapter
	mints     map[network.Network]string
	now       func() time.Time
}

func New(st Store, keys Keys, snaps Snapshots, blocks Blockhashes, tl Timeline,
	adapter chain.Adapter, mints map[network.Network]string, now func() time.Time) *Signer {
	return &Signer{
		store: st, keys: keys, snapshots: snaps, blocks: blocks,
		timeline: tl, adapter: adapter, mints: mints, now: now,
	}
}

// Request is what an agent presents.
type Request struct {
	APIKey string
	Host   string
	// IdempotencyKey identifies THIS ATTEMPT. A retry carrying the same one replays the original
	// answer; a new purchase carries a new one and is signed afresh. The agent is the only party
	// that knows which it is doing, so it is the only party that can say.
	IdempotencyKey string
	// Challenge is the raw 402 body, exactly as the endpoint sent it. Passing it through
	// unparsed means the signer reads the same bytes the agent received, rather than a
	// re-serialisation that could differ.
	Challenge []byte
	TraceID   string
}

// Response is what it gets back when the payment is allowed.
type Response struct {
	PaymentID string `json:"payment_id"`
	Signature string `json:"signature"`
	// Payload is the credential header VALUE — standard base64 of the x402 envelope.
	Payload string `json:"payload"`
	// Header is the header NAME to put it in. It differs between protocol versions, and a
	// credential in the wrong header is simply not seen.
	Header string `json:"header"`
	// Memo is how this payment is found on chain once a facilitator has settled it. The signature
	// above is the agent's; a sponsored transaction is known by the fee payer's, which does not
	// exist yet.
	Memo      string         `json:"memo"`
	Allowance map[string]any `json:"allowance"`
}

// Sign runs the ordered path from docs/06-signer.md.
func (s *Signer) Sign(ctx context.Context, req Request) (*Response, error) {
	now := s.now()

	// The three phases the signer writes are three DIFFERENT moments, and the timeline is read by
	// somebody reconciling a payment. Stamping them all with one time would make the order
	// arbitrary and the durations invisible, so each is recorded when it actually happened.
	var at struct{ received, evaluated, signed time.Time }

	// 2 · canonicalise and hash. Pure. An unknown x402 version stops here rather than reaching the
	//     hash, because guessing its canonical form would produce a different hash for the same
	//     challenge — which is a double payment, not a formatting difference.
	reqs, err := challenge.ParseRequirements(req.Challenge)
	if err != nil {
		return nil, err
	}
	// The first offer. Selecting among several is a client-side preference (cheapest on the
	// preferred network), and the agent has already made it by the time the challenge reaches us.
	ch, err := challenge.Select(reqs, reqs.Accepts[0], req.Host)
	if err != nil {
		return nil, err
	}
	offerHash := ch.Hash()
	at.received = s.now()

	// 4 · the snapshot. Zero round trips when warm.
	keyHash := hashKey(req.APIKey)
	snap, err := s.snapshots.Get(ctx, keyHash, now)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, fault.KeyRevoked
		}
		return nil, err
	}

	// The network the key CLAIMS, from its prefix. S0 compares it against the agent's own.
	keyNet, err := network.FromKeyPrefix(req.APIKey)
	if err != nil {
		return nil, fault.NetworkMismatch.WithMessage("the API key does not name a network")
	}

	blockhash, ok := s.blocks.Get(snap.Agent.Network)
	if !ok {
		// Refusing is correct: signing against a stale blockhash produces a transaction that will
		// not land, and the agent would treat it as a payment that happened.
		return nil, fault.ChainUnreachable.WithMessage(
			"no recent blockhash for " + snap.Agent.Network.String())
	}

	// 3 · claim. ONE round trip, and the database decides the race.
	//
	// Keyed on (agent, attempt, offer) rather than the offer alone — x402 challenges carry no
	// nonce, so two purchases of the same resource are byte-identical and hashing the offer alone
	// would hand the second one the first one's signature.
	claimKey := challenge.ClaimKey(snap.Agent.ID, req.IdempotencyKey, ch)
	claim, err := s.store.ClaimChallenge(ctx, claimKey, snap.Agent.ID, snap.Agent.Network, blockhash, now)
	if err != nil {
		return nil, err
	}
	switch {
	case claim.Replay != nil:
		// A retry of a settled challenge gets the ORIGINAL answer. This is invariant I7 as the
		// agent experiences it: the same challenge, the same signature, once.
		return replayOf(claim.Replay), nil
	case claim.InFlight:
		return nil, fault.SignInProgress
	}
	// The winner signs against the blockhash it pinned. A retry after a crash rebuilds the
	// identical message, so re-signing produces byte-identical output.
	blockhash = claim.Blockhash

	// 5 · evaluate. Pure, over the cache. S0, S1, S2, S3, S6 and S7 are decided here.
	psnap := toPolicySnapshot(snap, keyNet, s.mints[snap.Agent.Network])
	preq := policy.Request{
		Host: ch.Host, PayTo: ch.PayTo, Amount: ch.Amount, Mint: ch.Asset,
		Scheme: ch.Scheme, Network: ch.Network,
		// Checked at build time. Under the `exact` scheme, creating the recipient's account is the
		// endpoint's job, so a missing one is refused with an explanation it can act on.
		RecipientAccountExists: true,
	}
	result := policy.Evaluate(psnap, preq, now)
	at.evaluated = s.now()

	if !result.Allowed() {
		return nil, s.recordBlock(ctx, snap, ch, offerHash, claimKey, result, req.TraceID, now)
	}

	// 6 · the compare-and-swap. ONE round trip. S4 and S5, atomically.
	admitted, err := s.store.AdmitDraw(ctx, snap.Agent.Network, snap.Allowance.ID,
		ch.Amount, snap.Policy.VelocityMax, snap.Policy.VelocityWindow, now)
	if err != nil {
		if err == store.ErrNotAdmitted {
			// The cache said allowed and the database said no: that IS the race. Re-read and
			// re-run the pure engine to name the rule precisely. One extra round trip, on the rare
			// losing path, is the right place to spend it.
			return nil, s.nameTheRaceLoser(ctx, keyHash, snap, ch, offerHash, claimKey, req.TraceID, now)
		}
		return nil, err
	}

	// 7 · the key. Zero round trips when warm; a KMS unwrap when cold.
	priv, err := s.keys.For(ctx, snap.Agent.ID)
	if err != nil {
		return nil, err
	}

	// 8 · build and sign. Deterministic, given the pinned blockhash.
	// The facilitator sponsors the fee, from the offer. If the offer names none, the agent pays —
	// and pay-kit refuses a transaction where that payer is also the authority, which BuildDraw
	// checks before producing a signature that would be thrown away.
	feePayer, _ := ch.FeePayer()
	memo, _ := ch.Offer.Memo()

	signed, err := s.adapter.BuildDraw(ctx, chain.DrawParams{
		AllowanceAcc: snap.Allowance.OnchainAddr,
		AgentPubkey:  snap.Agent.Pubkey,
		AgentSign:    edSigner{priv: priv, pub: snap.Agent.Pubkey},
		PayTo:        ch.PayTo,
		Mint:         ch.Asset,
		Program:      snap.Allowance.TokenProgram,
		Amount:       ch.Amount,
		Decimals:     6, // USDC
		FeePayer:     feePayer,
		Memo:         memo,
		Blockhash:    blockhash,
		Network:      snap.Agent.Network,
	})
	if err != nil {
		return nil, fmt.Errorf("building the draw: %w", err)
	}
	at.signed = s.now()

	// 9 · record. ONE round trip.
	srID, payID, err := s.store.RecordVerdict(ctx, store.SignRequest{
		AgentID: snap.Agent.ID, Network: snap.Agent.Network, ChallengeHash: claimKey,
		Verdict: state.VerdictAllowed, Checks: result.Checks,
		RequirementsRaw: ch.Canonical(), Warnings: result.Warnings,
		Requirements: map[string]any{
			"host": ch.Host, "pay_to": ch.PayTo, "amount": ch.Amount.String(),
			"scheme": ch.Scheme, "mint": ch.Asset, "resource": ch.Resource,
			"x402_version": int(ch.Version),
		},
	}, &store.Payment{
		AgentID: snap.Agent.ID, AgentName: snap.Agent.Name, AllowanceID: snap.Allowance.ID,
		OrgID: snap.Agent.OrgID, Signature: signed.Signature, Memo: signed.Memo,
		PayTo: ch.PayTo, Host: ch.Host, Amount: ch.Amount,
	}, now)
	if err != nil {
		return nil, err
	}

	// The agent needs a CREDENTIAL, not a transaction: the header value is base64 of an envelope
	// that names the version and wraps the transaction. Building it here rather than in the agent
	// means the shape follows the challenge we actually read, and an SDK on the other side is
	// never asked to guess which version we answered.
	credential, err := ch.Credential(signed.Base64)
	if err != nil {
		return nil, err
	}

	resp := &Response{
		PaymentID: payID,
		Signature: signed.Signature,
		Payload:   credential,
		Header:    ch.PaymentHeaderName(),
		Memo:      signed.Memo,
		Allowance: map[string]any{
			"remaining":    admitted.Remaining().String(),
			"read_at_slot": fmt.Sprint(admitted.LastReadSlot),
		},
	}

	// 11 · the timeline, AFTER the verdict is computed. Fire and forget onto a bounded queue.
	// Writing it synchronously would put an audit write on the critical path of a payment; an
	// unbounded queue would turn a database stall into an out-of-memory kill.
	s.enqueuePhases(snap, payID, result, ch, req.TraceID, at.received, at.evaluated, at.signed)
	go func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.store.SettleClaim(c, claimKey, map[string]any{
			"payment_id": payID, "signature": signed.Signature, "payload": credential,
			"header": ch.PaymentHeaderName(), "memo": signed.Memo,
			"remaining": admitted.Remaining().String(),
		}, srID, now)
	}()

	return resp, nil
}

// recordBlock writes the append-only verdict for a refusal and returns the fault.
//
// A block is the product working, and it is recorded as carefully as a payment: the rule that
// fired, every check that ran, and the ones that were never reached.
func (s *Signer) recordBlock(ctx context.Context, snap store.Snapshot, ch challenge.Challenge,
	offerHash, claimKey string, result policy.Result, traceID string, now time.Time) error {

	srID, _, err := s.store.RecordVerdict(ctx, store.SignRequest{
		AgentID: snap.Agent.ID, Network: snap.Agent.Network, ChallengeHash: claimKey,
		Verdict: state.VerdictBlocked, FailedRule: result.FailedRule.String(),
		Checks: result.Checks, RequirementsRaw: ch.Canonical(), Warnings: result.Warnings,
		// The host is recorded separately from the canonical form, which deliberately excludes it
		// — the same challenge is the same payment whichever name resolved to the server. But the
		// recovery button needs to name exactly the host that was refused, so it is stored here.
		Requirements: map[string]any{
			"host": ch.Host, "pay_to": ch.PayTo, "amount": ch.Amount.String(),
			"scheme": ch.Scheme, "mint": ch.Asset, "resource": ch.Resource,
			"x402_version": int(ch.Version), "offer_hash": offerHash,
		},
	}, nil, now)
	if err != nil {
		return err
	}

	go func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.store.SettleClaim(c, claimKey, map[string]any{
			"blocked": true, "code": result.Fault.Code(), "rule": result.FailedRule.String(),
		}, srID, now)
	}()

	return result.Fault.WithDetails(map[string]any{
		"rule":         result.FailedRule.String(),
		"tier":         string(result.Checks[int(result.FailedRule)].Tier),
		"checks":       result.Checks,
		"checks_total": len(result.Checks),
	})
}

// nameTheRaceLoser re-reads and re-evaluates so the refusal names S4 or S5 precisely rather than
// saying "one of two things".
func (s *Signer) nameTheRaceLoser(ctx context.Context, keyHash string, snap store.Snapshot,
	ch challenge.Challenge, offerHash, claimKey string, traceID string, now time.Time) error {

	// Re-read PAST the cache: the cached snapshot is what said "allowed", so consulting it again
	// would give the same wrong answer. Eviction forces a fresh read of the allowance the
	// compare-and-swap just refused.
	s.snapshots.Evict(snap.Agent.ID)
	fresh, err := s.snapshots.Get(ctx, keyHash, now)
	if err != nil {
		// Even the re-read failed. Report the budget, which is the likelier of the two and the one
		// an owner can act on.
		return fault.BudgetExhausted
	}
	// The key's network is the agent's here: S0 already passed, or we would not have reached the
	// compare-and-swap at all.
	psnap := toPolicySnapshot(fresh, fresh.Agent.Network, s.mints[fresh.Agent.Network])
	result := policy.Evaluate(psnap, policy.Request{
		Host: ch.Host, PayTo: ch.PayTo, Amount: ch.Amount, Mint: ch.Asset,
		Scheme: ch.Scheme, Network: ch.Network, RecipientAccountExists: true,
	}, now)
	if result.Allowed() {
		// It fits again — another payment resolved in between. The honest answer is still that
		// THIS attempt did not get in.
		return fault.BudgetExhausted
	}
	return s.recordBlock(ctx, fresh, ch, offerHash, claimKey, result, traceID, now)
}

func (s *Signer) enqueuePhases(snap store.Snapshot, paymentID string, result policy.Result,
	ch challenge.Challenge, traceID string, received, evaluated, signed time.Time) {
	if paymentID == "" {
		return
	}
	passed := 0
	for _, c := range result.Checks {
		if c.Result == policy.Pass {
			passed++
		}
	}
	for _, e := range []store.HandshakeEvent{
		{PaymentID: paymentID, Network: snap.Agent.Network, Phase: state.PhaseChallengeReceived,
			Writer: state.WriterSigner, TraceID: traceID, At: received,
			Detail: map[string]any{"scheme": ch.Scheme, "amount": ch.Amount.String()}},
		{PaymentID: paymentID, Network: snap.Agent.Network, Phase: state.PhaseRulesEvaluated,
			Writer: state.WriterSigner, TraceID: traceID, At: evaluated,
			// The count comes from the array, never a literal — which is how the interface's "N of
			// N" cannot drift from the engine again.
			Detail: map[string]any{"passed": passed, "total": len(result.Checks)}},
		{PaymentID: paymentID, Network: snap.Agent.Network, Phase: state.PhaseSigned,
			Writer: state.WriterSigner, TraceID: traceID, At: signed,
			Detail: map[string]any{
				"allowance": snap.Allowance.OnchainAddr,
				// How long the signer actually took, for the budget in docs/04.
				"signer_ms": signed.Sub(received).Milliseconds(),
			}},
	} {
		s.timeline.Enqueue(e)
	}
}

func toPolicySnapshot(s store.Snapshot, keyNet network.Network, mint string) policy.Snapshot {
	return policy.Snapshot{
		Agent: policy.AgentView{
			Network: s.Agent.Network, Killed: s.Agent.Killed,
		},
		Policy: policy.PolicyView{
			AllowHosts: s.Policy.AllowHosts, AllowPayTo: s.Policy.AllowPayTo,
			AllowAll: s.Policy.AllowAll, PerTxMax: s.Policy.PerTxMax,
			VelocityMax: s.Policy.VelocityMax, VelocityWindow: s.Policy.VelocityWindow,
		},
		Allowance: policy.AllowanceView{
			State: s.Allowance.State, Cap: s.Allowance.Cap, Drawn: s.Allowance.Drawn,
			Reserved: s.Allowance.Reserved, ExpiryTS: s.Allowance.ExpiryTS,
			ExpiryTier: s.Allowance.ExpiryTier, Mint: s.Allowance.Mint,
			LastReadSlot: s.Allowance.LastReadSlot, Velocity: s.Velocity,
		},
		KeyNetwork:   keyNet,
		ExpectedMint: mint,
		TakenAt:      s.TakenAt,
	}
}

func replayOf(m map[string]any) *Response {
	r := &Response{Allowance: map[string]any{}}
	if v, ok := m["payment_id"].(string); ok {
		r.PaymentID = v
	}
	if v, ok := m["signature"].(string); ok {
		r.Signature = v
	}
	if v, ok := m["payload"].(string); ok {
		r.Payload = v
	}
	if v, ok := m["header"].(string); ok {
		r.Header = v
	}
	if v, ok := m["memo"].(string); ok {
		r.Memo = v
	}
	if v, ok := m["remaining"].(string); ok {
		r.Allowance["remaining"] = v
	}
	return r
}

// edSigner adapts a private key to the adapter's signing interface, so the adapter is handed the
// ability to sign rather than the key itself.
type edSigner struct {
	priv ed25519.PrivateKey
	pub  string
}

func (e edSigner) PublicKey() string { return e.pub }
func (e edSigner) Sign(message []byte) ([]byte, error) {
	return ed25519.Sign(e.priv, message), nil
}
