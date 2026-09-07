// Package indexer is what makes invariants I2 and I4 true.
//
// It is the only component that polls outward, the only writer of `confirmed`, and the only thing
// that changes an allowance's cached numbers. Everything here is a loop; none of it is a request
// handler, which is why it is a separate long-running process.
package indexer

import (
	"context"
	"log/slog"
	"time"

	"github.com/gagliardetto/solana-go"

	"github.com/stone7890/leash/internal/chain"
	"github.com/stone7890/leash/internal/chain/rpc"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/domain/state"
	"github.com/stone7890/leash/internal/jobs"
	"github.com/stone7890/leash/internal/store"
)

type Indexer struct {
	store   *store.Store
	adapter chain.Adapter
	pool    *rpc.Pool

	// The internal surface: building unsigned transactions, submitting owner-signed ones, and
	// running the onboarding test payment. Reachable only from inside the network, and only with
	// the shared token — nginx exposes exactly one path of this process, the SSE stream.
	internalToken string
	signerURL     string
	demoURL       string
	mints         map[network.Network]MintConfig
}

// MintConfig is a network's USDC mint and the token program that owns it. USDC is classic SPL
// rather than Token-2022, and using the wrong program produces a confusing failure rather than an
// obvious one — so the program travels with the mint.
type MintConfig struct {
	Address string
	Program string
}

type Options struct {
	Pool          *rpc.Pool
	InternalToken string
	SignerURL     string
	DemoURL       string
	Mints         map[network.Network]MintConfig
}

func New(st *store.Store, a chain.Adapter, opts Options) *Indexer {
	return &Indexer{
		store: st, adapter: a, pool: opts.Pool,
		internalToken: opts.InternalToken, signerURL: opts.SignerURL,
		demoURL: opts.DemoURL, mints: opts.Mints,
	}
}

func (ix *Indexer) mintFor(n network.Network) (mint string, program string) {
	m := ix.mints[n]
	return m.Address, m.Program
}

// Jobs builds the four loops, one set per network.
func (ix *Indexer) Jobs(nets []network.Network, allowanceEvery, sweepEvery, alertEvery,
	faucetEvery time.Duration) []jobs.Job {
	var out []jobs.Job
	for _, n := range nets {
		n := n
		out = append(out,
			jobs.Job{Name: "allowance-refresh", Network: n, Every: allowanceEvery,
				Run: func(c context.Context, now time.Time) error {
					return ix.RefreshAllowances(c, n, now)
				}},
			jobs.Job{Name: "payment-sweep", Network: n, Every: sweepEvery,
				Run: func(c context.Context, now time.Time) error {
					return ix.SweepPayments(c, n, now)
				}},
			jobs.Job{Name: "alert-eval", Network: n, Every: alertEvery,
				Run: func(c context.Context, now time.Time) error {
					return ix.EvaluateAlerts(c, n, now)
				}},
		)
		// The faucet guard is sandbox only. Spawning it for mainnet would be a loop with nothing
		// to do, and the configuration treats that as an error rather than starting it.
		if n.IsSandbox() {
			out = append(out, jobs.Job{Name: "sandbox-faucet-guard", Network: n, Every: faucetEvery,
				Run: func(c context.Context, now time.Time) error {
					return ix.GuardFaucet(c, n, now)
				}})
		}
	}
	return out
}

// RefreshAllowances re-reads every allowance that can still change.
//
// This is the ONLY path from pending to active. The schema refuses an active allowance with no
// slot, so an allowance cannot become spendable without a read-back — invariant I4 expressed as
// "there is no other function".
func (ix *Indexer) RefreshAllowances(ctx context.Context, n network.Network, now time.Time) error {
	list, err := ix.store.AllowancesToRefresh(ctx, n, 200)
	if err != nil {
		return err
	}
	for _, a := range list {
		onchain, err := ix.adapter.ReadAllowance(ctx, n, a.OnchainAddr)
		if err != nil {
			// An RPC failure makes the data LATE, never wrong. Leave the allowance as it is and
			// come back in a minute.
			slog.Warn("could not read an allowance back",
				"network", n, "allowance_id", a.ID, "err", err)
			continue
		}
		if !onchain.Exists {
			// The delegation has not landed yet. Staying pending is correct: we do not spend
			// against something we have not seen.
			continue
		}

		next := a.State
		switch {
		case onchain.State == state.AllowanceRevoked:
			// Caught out of band — the owner may have revoked from their wallet or the CLI without
			// telling us. That is a supported path, and the whole point of I5.
			next = state.AllowanceRevoked
		case !a.ExpiryTS.IsZero() && !now.Before(a.ExpiryTS):
			next = state.AllowanceExpired
		case a.State == state.AllowancePending:
			next = state.AllowanceActive
			slog.Info("allowance activated after read-back",
				"network", n, "allowance_id", a.ID, "slot", onchain.Slot)
		}

		// The delegated amount left ON CHAIN is what remains of the cap, so what has been drawn is
		// the difference. Reading it rather than accumulating it is what makes the chain the source
		// of truth rather than our own arithmetic.
		drawn := a.Drawn
		if onchain.Cap > 0 && a.Cap >= onchain.Cap {
			drawn = a.Cap - onchain.Cap
		}

		if err := ix.store.RefreshFromChain(ctx, n, a.ID, drawn, next, onchain.Slot, now); err != nil {
			slog.Error("could not write a read-back",
				"network", n, "allowance_id", a.ID, "err", err)
			continue
		}

		// The compensating control for the one thing MongoDB made worse. `reserved` is a stored
		// derived value, and a stored derived value can drift. Drift is a bug — a payment refused
		// that should have gone through, or one admitted that should not — so it is measured every
		// minute and reported with its size.
		stored, actual, err := ix.store.RecomputeReserved(ctx, n, a.ID)
		if err != nil {
			continue
		}
		if stored != actual {
			slog.Warn("the stored reservation drifted from the payments behind it",
				"network", n, "allowance_id", a.ID,
				"stored", stored.String(), "actual", actual.String(),
				"delta", (stored - actual).String())
			if err := ix.store.SetReserved(ctx, n, a.ID, actual); err != nil {
				slog.Error("could not repair the reservation", "allowance_id", a.ID, "err", err)
			}
		}
	}
	return nil
}

// unresolvedFor is how long a payment may sit before the sweep looks at it. Long enough that a
// payment confirming normally is not swept while it is perfectly healthy.
const unresolvedFor = 90 * time.Second

// abandonedAfter is when a signature with no trace anywhere becomes `failed`.
//
// Deliberately long. A signature that has not landed in a day is genuinely gone, and shortening
// this trades a real risk — freeing budget for money that later moves — against tidiness.
const abandonedAfter = 24 * time.Hour

// SweepPayments is invariant I4, as a loop.
//
// Every exit from signed, submitted and unknown is a READ-BACK BY SIGNATURE. There is no branch
// here that re-signs, and none that infers an outcome from silence.
func (ix *Indexer) SweepPayments(ctx context.Context, n network.Network, now time.Time) error {
	list, err := ix.store.PaymentsInFlight(ctx, n, now.Add(-unresolvedFor), 200)
	if err != nil {
		return err
	}
	for _, p := range list {
		conf, err := ix.look(ctx, n, p)
		if err != nil {
			slog.Warn("could not check a payment", "network", n, "payment_id", p.ID, "err", err)
			continue
		}

		switch {
		case conf.Confirmed && conf.Err == "":
			if conf.Signature != "" && conf.Signature != p.Signature {
				// A sponsored payment settled under the FEE PAYER's signature, which we could not
				// have known when we signed. Record it, so the audit trail names the transaction
				// an explorer would show rather than one that exists nowhere.
				_ = ix.store.SetSettledSignature(ctx, n, p.ID, conf.Signature)
			}
			ix.confirm(ctx, n, p, conf.Slot, now)

		case conf.Found && conf.Err != "":
			// The chain rejected it. That is a definite outcome, so the budget goes back.
			slog.Info("a payment was rejected on chain",
				"network", n, "payment_id", p.ID, "err", conf.Err)
			_ = ix.store.SetPaymentState(ctx, n, p.ID, state.PaymentFailed, 0, now)
			_ = ix.store.ReleaseReservation(ctx, n, p.AllowanceID, p.Amount)

		case conf.Found:
			_ = ix.store.SetPaymentState(ctx, n, p.ID, state.PaymentSubmitted, 0, now)
			_ = ix.store.AppendPhase(ctx, store.HandshakeEvent{
				PaymentID: p.ID, Network: n, Phase: state.PhaseBroadcast,
				Writer: state.WriterIndexer, At: now,
			})

		case now.Sub(p.CreatedAt) > abandonedAfter:
			// A day with no trace. Now it is failed, and it is worth a human look.
			slog.Warn("a payment left no trace for 24 hours",
				"network", n, "payment_id", p.ID, "signature", p.Signature)
			_ = ix.store.SetPaymentState(ctx, n, p.ID, state.PaymentFailed, 0, now)
			_ = ix.store.ReleaseReservation(ctx, n, p.AllowanceID, p.Amount)

		default:
			// We have not seen it. NOT a failure — it keeps holding its money and is re-checked,
			// because assuming failure and freeing the budget is how one challenge gets paid twice.
			_ = ix.store.SetPaymentState(ctx, n, p.ID, state.PaymentUnknown, 0, now)
		}
	}
	return nil
}

// look asks the chain what happened to a payment.
//
// TWO ways, because there are two kinds of payment. An unsponsored one is known by the signature
// we produced, and a status lookup finds it. A SPONSORED one is known by the fee payer's
// signature, which did not exist when we signed — so it is found by the memo it carried, scanning
// the account it was drawn from. Without the second path a sponsored payment would sit in
// `unknown` until the sweep abandoned it a day later, having moved money the whole time.
func (ix *Indexer) look(ctx context.Context, n network.Network, p store.Payment) (
	chain.Confirmation, error) {
	if _, err := solana.SignatureFromBase58(p.Signature); p.Signature != "" && err != nil {
		// A signature that does not decode can never be read back, so asking the chain about it
		// returns the same error for ever. Reported as an error, that would make the sweep
		// `continue` on every pass: the payment never reaches the 24-hour branch, its reservation
		// is never released, and the allowance loses that money permanently to a WARN nobody acts
		// on. It is not a chain error — it is an unreadable record, so it is treated as "we have
		// not seen it" and follows the ordinary unknown → abandoned path.
		slog.Warn("a payment carries a signature that does not decode; it can only be resolved "+
			"by the memo, or abandoned",
			"network", n, "payment_id", p.ID, "signature", p.Signature, "err", err)
	} else if p.Signature != "" {
		conf, err := ix.adapter.SignatureStatus(ctx, n, p.Signature)
		if err == nil && conf.Found {
			return conf, nil
		}
		if err != nil && p.Memo == "" {
			// A live RPC error, which is transient by assumption: the sweep skips this payment and
			// tries again next tick rather than guessing at an outcome.
			return chain.Confirmation{}, err
		}
	}
	if p.Memo == "" {
		return chain.Confirmation{Found: false}, nil
	}
	alw, err := ix.store.LiveAllowance(ctx, p.AgentID)
	if err != nil {
		// The allowance may have been revoked since. Not being able to look is not the same as
		// having looked and found nothing.
		return chain.Confirmation{Found: false}, nil
	}
	return ix.adapter.SignatureByMemo(ctx, n, alw.OnchainAddr, p.Memo)
}

// confirm writes the two phases and the money, in the right order.
func (ix *Indexer) confirm(ctx context.Context, n network.Network, p store.Payment,
	slot int64, now time.Time) {

	// Phase 5 may already exist if we saw it submitted first. A duplicate is refused by the index
	// and reported as success, which is what makes this safely re-runnable.
	_ = ix.store.AppendPhase(ctx, store.HandshakeEvent{
		PaymentID: p.ID, Network: n, Phase: state.PhaseBroadcast,
		Writer: state.WriterIndexer, At: now,
	})

	if err := ix.store.SetPaymentState(ctx, n, p.ID, state.PaymentConfirmed, slot, now); err != nil {
		slog.Error("could not confirm a payment", "payment_id", p.ID, "err", err)
		return
	}

	// The reservation is released ONLY here, in the same operation that writes what the chain now
	// says was drawn — so the money is never in neither term.
	alw, err := ix.store.LiveAllowance(ctx, p.AgentID)
	if err == nil {
		onchain, rerr := ix.adapter.ReadAllowance(ctx, n, alw.OnchainAddr)
		drawn := alw.Drawn + p.Amount
		if rerr == nil && onchain.Exists && onchain.Cap > 0 && alw.Cap >= onchain.Cap {
			drawn = alw.Cap - onchain.Cap
		}
		if err := ix.store.ConfirmDraw(ctx, n, alw.ID, p.Amount, drawn, slot, now); err != nil {
			slog.Error("could not settle the reservation", "payment_id", p.ID, "err", err)
		}
	}

	// Only the indexer may write this phase, and only with the slot. The schema refuses anything
	// else — which is invariant I4 as a database constraint rather than a code review.
	_ = ix.store.AppendPhase(ctx, store.HandshakeEvent{
		PaymentID: p.ID, Network: n, Phase: state.PhaseConfirmed,
		Writer: state.WriterIndexer, ReadBackSlot: slot, At: now,
		Detail: map[string]any{"amount": p.Amount.String()},
	})

	slog.Info("payment confirmed",
		"network", n, "payment_id", p.ID, "slot", slot, "amount", p.Amount.String())
}

// EvaluateAlerts runs every 30 seconds, because the interface promises alerts within thirty
// seconds of the on-chain event they describe. The cadence is a contract, not a preference.
func (ix *Indexer) EvaluateAlerts(ctx context.Context, n network.Network, now time.Time) error {
	list, err := ix.store.AllowancesToRefresh(ctx, n, 500)
	if err != nil {
		return err
	}
	for _, a := range list {
		if a.State != state.AllowanceActive || a.Cap == 0 {
			continue
		}
		spent := a.Cap - a.Remaining()
		pct := int(float64(spent) / float64(a.Cap) * 100)
		switch {
		case pct >= 95:
			ix.raise(ctx, a, "budget_critical", n, now, pct)
		case pct >= 80:
			ix.raise(ctx, a, "budget_warning", n, now, pct)
		}
		if a.ExpiringSoon(now) {
			ix.raise(ctx, a, "expiring_soon", n, now, pct)
		}
	}
	return nil
}

func (ix *Indexer) raise(ctx context.Context, a store.Allowance, kind string,
	n network.Network, now time.Time, pct int) {
	agent, err := ix.store.GetAgent(ctx, a.AgentID)
	if err != nil {
		return
	}
	// One alert per situation, not one per evaluation. Without the dedupe key a crossed threshold
	// would notify an owner every thirty seconds until they acted.
	_ = ix.store.RaiseAlert(ctx, store.Alert{
		OrgID: agent.OrgID, AgentID: a.AgentID, Network: n, Kind: kind,
		DedupeKey: kind + "|" + a.ID + "|" + a.State.String(),
		Body: map[string]any{
			"agent": agent.Name, "percent": pct,
			"spent": (a.Cap - a.Remaining()).String(), "cap": a.Cap.String(),
		},
		CreatedAt: now,
	})
}

// GuardFaucet releases stale sign claims and keeps the sandbox tidy.
//
// The sandbox has no other limits by design: it is play money and the point is that a developer
// can spend it freely.
func (ix *Indexer) GuardFaucet(ctx context.Context, _ network.Network, now time.Time) error {
	// A claim in flight with nothing behind it is a crash between claiming and recording. Sixty
	// seconds is long enough that a slow signature is not interrupted.
	freed, err := ix.store.ReleaseStaleClaims(ctx, now.Add(-60*time.Second))
	if err != nil {
		return err
	}
	if freed > 0 {
		slog.Info("released stale sign claims", "count", freed)
	}
	return nil
}

var _ = money.One
