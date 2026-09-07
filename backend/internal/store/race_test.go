package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/domain/state"
	"github.com/stone7890/leash/internal/domain/tier"
	"github.com/stone7890/leash/internal/store"
	"github.com/stone7890/leash/internal/store/mongotest"
)

const (
	testMint    = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	testProgram = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	testWallet  = "7f3KFa7tyBX3BvBCfmDmXDar8CRq5HunjxE4vU4hEgXa"
	testPubkey  = "Gk7vGySf1TALm7PsiwGJ6f22s9SGi99sdfx9QyWtavWE"
	testAcct    = "3xk9hSScqsuG9StMc2Qw5536QSdMp74Kndjtv2yS8qU2"
)

// liveAllowance builds an org, an agent and an ACTIVE allowance with the given cap.
func liveAllowance(t *testing.T, st *store.Store, cap money.Base, velMax money.Base) (
	org store.Org, agent store.Agent, alw store.Allowance) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	org, err := st.UpsertOrg(ctx, testWallet, now)
	if err != nil {
		t.Fatalf("org: %v", err)
	}
	agent, err = st.CreateAgent(ctx, store.NewAgent{
		OrgID: org.ID, Name: "research-bot-01", Template: "research",
		Network: network.Sandbox, RunsAs: "cli", Pubkey: testPubkey,
		IdempotencyKey: "idem-" + time.Now().Format("150405.000000000"),
		KeyHash:        "0000000000000000000000000000000000000000000000000000000000000001",
		WrappedKey:     []byte("not-a-real-key"), KMSKeyRef: "local",
		Policy: store.Policy{
			AllowHosts: []string{"api.exa.ai"}, PerTxMax: money.One,
			VelocityMax: velMax, VelocityWindow: 10 * time.Minute,
		},
		ExpiryTier: tier.Signer,
	}, now)
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	alw, err = st.CreateAllowance(ctx, store.NewAllowance{
		AgentID: agent.ID, Network: network.Sandbox, OnchainAddr: testAcct,
		Mint: testMint, TokenProgram: testProgram, Cap: cap,
		ExpiryTS: now.Add(7 * 24 * time.Hour), ExpiryTier: tier.Signer,
	}, now)
	if err != nil {
		t.Fatalf("allowance: %v", err)
	}
	// Only a read-back makes an allowance spendable. The schema refuses an active one with no
	// slot, so this is the only way to get there — which is invariant I4 as a structural fact.
	if err := st.RefreshFromChain(ctx, network.Sandbox, alw.ID, 0,
		state.AllowanceActive, 356442108, now); err != nil {
		t.Fatalf("read-back: %v", err)
	}
	return org, agent, alw
}

// THE test. Sixty-four goroutines race for a budget that fits exactly one of them.
//
// Exactly one must win. Not "usually one" — the whole design of AdmitDraw exists so that the
// losers' filters simply do not match, rather than all of them reading the same remaining balance
// and all deciding they fit.
func TestManyRequestsRacingForTheLastCentAdmitExactlyOne(t *testing.T) {
	st := mongotest.New(t)
	ctx := context.Background()

	const amount = money.Base(10_000) // $0.01
	_, _, alw := liveAllowance(t, st, amount, money.Base(1_000_000_000))

	const racers = 64
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
		refused  int
		other    []error
	)
	start := make(chan struct{})
	now := time.Now().UTC()

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them all at once, to make the race as tight as possible
			_, err := st.AdmitDraw(ctx, network.Sandbox, alw.ID, amount,
				money.Base(1_000_000_000), 10*time.Minute, now)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				admitted++
			case errors.Is(err, store.ErrNotAdmitted):
				refused++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range other {
		t.Errorf("unexpected error: %v", err)
	}
	if admitted != 1 {
		t.Fatalf("%d requests were admitted, want exactly 1 (%d refused)", admitted, refused)
	}
	if refused != racers-1 {
		t.Errorf("%d refused, want %d", refused, racers-1)
	}

	// And the invariant the schema enforces must hold afterwards.
	var alwAfter store.Allowance
	alwAfter, err := st.LiveAllowance(ctx, alw.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if alwAfter.Drawn+alwAfter.Reserved > alwAfter.Cap {
		t.Fatalf("drawn(%s) + reserved(%s) exceeds cap(%s)",
			alwAfter.Drawn, alwAfter.Reserved, alwAfter.Cap)
	}
	if alwAfter.Reserved != amount {
		t.Errorf("reserved is %s, want exactly one payment's worth (%s)",
			alwAfter.Reserved, amount)
	}
	if alwAfter.Remaining() != 0 {
		t.Errorf("remaining is %s, want 0", alwAfter.Remaining())
	}
}

// The budget must be spent to the last unit, and not one beyond it.
func TestABudgetAdmitsExactlyAsManyPaymentsAsItHolds(t *testing.T) {
	st := mongotest.New(t)
	ctx := context.Background()

	const amount = money.Base(10_000)
	const fits = 7
	_, _, alw := liveAllowance(t, st, amount*fits, money.Base(1_000_000_000))

	now := time.Now().UTC()
	admitted := 0
	for i := 0; i < fits+3; i++ {
		if _, err := st.AdmitDraw(ctx, network.Sandbox, alw.ID, amount,
			money.Base(1_000_000_000), 10*time.Minute, now); err == nil {
			admitted++
		}
	}
	if admitted != fits {
		t.Fatalf("%d payments admitted, want exactly %d", admitted, fits)
	}
}

// The velocity window is decided by the same atomic operation as the budget, because both depend
// on state that concurrent requests are competing to change.
func TestTheVelocityWindowIsEnforcedByTheDatabase(t *testing.T) {
	st := mongotest.New(t)
	ctx := context.Background()

	const amount = money.Base(400_000) // $0.40
	velMax := money.One                // $1.00 in the window
	_, _, alw := liveAllowance(t, st, money.Base(100_000_000), velMax)

	now := time.Now().UTC()
	window := 10 * time.Minute

	// Two fit inside $1.00; the third does not.
	for i := 0; i < 2; i++ {
		if _, err := st.AdmitDraw(ctx, network.Sandbox, alw.ID, amount, velMax, window, now); err != nil {
			t.Fatalf("payment %d should have been admitted: %v", i+1, err)
		}
	}
	if _, err := st.AdmitDraw(ctx, network.Sandbox, alw.ID, amount, velMax, window, now); !errors.Is(err, store.ErrNotAdmitted) {
		t.Fatalf("the third payment should have exceeded the window, got %v", err)
	}

	// Ten minutes and one second later, the window has emptied.
	later := now.Add(window + time.Second)
	if _, err := st.AdmitDraw(ctx, network.Sandbox, alw.ID, amount, velMax, window, later); err != nil {
		t.Fatalf("after the window passed the payment should be admitted: %v", err)
	}
}

// The boundary, asserted against the REAL aggregation rather than the pure engine.
//
// The Go implementation and the MongoDB $filter are two implementations of one rule, and they can
// disagree. If they ever do, the database is right — it is what actually admits the payment — and
// the pure engine is the bug. This test is what would catch that.
func TestAnEntryExactlyAtTheWindowEdgeIsOutsideIt(t *testing.T) {
	st := mongotest.New(t)
	ctx := context.Background()

	const amount = money.Base(600_000) // $0.60
	velMax := money.One
	_, _, alw := liveAllowance(t, st, money.Base(100_000_000), velMax)

	base := time.Now().UTC().Truncate(time.Millisecond)
	window := 10 * time.Minute

	if _, err := st.AdmitDraw(ctx, network.Sandbox, alw.ID, amount, velMax, window, base); err != nil {
		t.Fatalf("the first payment should be admitted: %v", err)
	}

	// Exactly one window later, the first entry sits ON the cutoff and must not count. Two
	// payments of $0.60 total $1.20, so if the boundary were inclusive this would be refused.
	edge := base.Add(window)
	if _, err := st.AdmitDraw(ctx, network.Sandbox, alw.ID, amount, velMax, window, edge); err != nil {
		t.Fatalf("an entry exactly at the window edge must be OUTSIDE it, but the payment was "+
			"refused: %v", err)
	}

	// One millisecond earlier, it is still inside, and the payment must be refused.
	st2 := mongotest.New(t)
	_, _, alw2 := liveAllowance(t, st2, money.Base(100_000_000), velMax)
	if _, err := st2.AdmitDraw(ctx, network.Sandbox, alw2.ID, amount, velMax, window, base); err != nil {
		t.Fatal(err)
	}
	justInside := base.Add(window - time.Millisecond)
	if _, err := st2.AdmitDraw(ctx, network.Sandbox, alw2.ID, amount, velMax, window, justInside); !errors.Is(err, store.ErrNotAdmitted) {
		t.Fatalf("an entry one millisecond inside the window must still count, got %v", err)
	}
}

// Drift in the stored reservation is the price MongoDB charged for having no row lock. It is
// repaired every 60 seconds, and the repair must actually find the discrepancy.
func TestALeakedReservationIsDetectedAndRepaired(t *testing.T) {
	st := mongotest.New(t)
	ctx := context.Background()

	const amount = money.Base(10_000)
	_, _, alw := liveAllowance(t, st, money.Base(10_000_000), money.Base(1_000_000_000))
	now := time.Now().UTC()

	// Reserve twice, but record no payments — the shape a crash between the swap and the write
	// leaves behind.
	for i := 0; i < 2; i++ {
		if _, err := st.AdmitDraw(ctx, network.Sandbox, alw.ID, amount,
			money.Base(1_000_000_000), 10*time.Minute, now); err != nil {
			t.Fatal(err)
		}
	}

	stored, actual, err := st.RecomputeReserved(ctx, network.Sandbox, alw.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored != 2*amount {
		t.Errorf("stored reservation is %s, want %s", stored, 2*amount)
	}
	if actual != 0 {
		t.Errorf("no payments were recorded, so the actual reservation should be 0, got %s", actual)
	}
	// The leak under-reports the budget — conservative, never overspending.
	if stored <= actual {
		t.Error("a leak must leave the stored reservation HIGHER than the truth, so the error " +
			"is always in the safe direction")
	}

	if err := st.SetReserved(ctx, network.Sandbox, alw.ID, actual); err != nil {
		t.Fatal(err)
	}
	after, err := st.LiveAllowance(ctx, alw.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Reserved != 0 {
		t.Errorf("after repair the reservation is %s, want 0", after.Reserved)
	}
}

// An agent cannot hold two live budgets for one mint. Two would mean two counters against one
// on-chain delegation, and it goes wrong only under concurrency — which is to say, at the demo.
func TestAnAgentCannotHoldTwoLiveAllowances(t *testing.T) {
	st := mongotest.New(t)
	ctx := context.Background()
	_, agent, _ := liveAllowance(t, st, money.One, money.One)

	_, err := st.CreateAllowance(ctx, store.NewAllowance{
		AgentID: agent.ID, Network: network.Sandbox,
		OnchainAddr: "6an25UDTVLeKctrD8udia6SXRnQU2ZkR1QESwD27UzfP",
		Mint:        testMint, TokenProgram: testProgram, Cap: money.One,
		ExpiryTS: time.Now().Add(time.Hour), ExpiryTier: tier.Signer,
	}, time.Now().UTC())
	if !store.IsDuplicate(err) {
		t.Fatalf("a second live allowance should be refused by the index, got %v", err)
	}
}

// Invariant I7 at the storage layer, and one of the four pieces of evidence.
func TestTheSameChallengeIsClaimedOnlyOnce(t *testing.T) {
	st := mongotest.New(t)
	ctx := context.Background()
	_, agent, _ := liveAllowance(t, st, money.One, money.One)

	const hash = "b177922b345e103f6cb62af767b824e61e128e3d1df50e08a38011bae3a82e07"
	now := time.Now().UTC()

	first, err := st.ClaimChallenge(ctx, hash, agent.ID, network.Sandbox, "blockhash-1", now)
	if err != nil || !first.Won {
		t.Fatalf("the first claim should win: %+v %v", first, err)
	}

	second, err := st.ClaimChallenge(ctx, hash, agent.ID, network.Sandbox, "blockhash-2", now)
	if err != nil {
		t.Fatal(err)
	}
	if second.Won {
		t.Fatal("two callers both won the same challenge — one API call would be paid twice")
	}
	if !second.InFlight {
		t.Error("the second caller should be told the challenge is in flight, so it retries with " +
			"the SAME challenge rather than making a new one")
	}
	// The blockhash is pinned by the WINNER. A retry rebuilds the identical message, which is what
	// makes re-signing after a crash produce one signature rather than two.
	if second.Blockhash != "blockhash-1" {
		t.Errorf("the pinned blockhash is %q, want the winner's", second.Blockhash)
	}

	// Once settled, a retry replays the stored answer verbatim.
	if err := st.SettleClaim(ctx, hash, map[string]any{"signature": "3xk9Qe"}, "sgr_test_x", now); err != nil {
		t.Fatal(err)
	}
	third, err := st.ClaimChallenge(ctx, hash, agent.ID, network.Sandbox, "blockhash-3", now)
	if err != nil {
		t.Fatal(err)
	}
	if third.Won || third.InFlight {
		t.Fatal("a settled challenge must replay, not re-run")
	}
	if third.Replay["signature"] != "3xk9Qe" {
		t.Errorf("the replay returned %v, want the original signature", third.Replay)
	}
}

// Every job body must be safely re-runnable, so a dropped tick needs no reasoning about.
func TestWritingTheSamePhaseTwiceIsHarmless(t *testing.T) {
	st := mongotest.New(t)
	ctx := context.Background()
	org, agent, alw := liveAllowance(t, st, money.One, money.One)
	now := time.Now().UTC()

	_, payID, err := st.RecordVerdict(ctx, store.SignRequest{
		AgentID: agent.ID, Network: network.Sandbox,
		ChallengeHash: "c288a33c456f214f7dc73bf878c935f72f239f4e2ef61f19b49122cbf4b93f18",
		Verdict:       state.VerdictAllowed,
	}, &store.Payment{
		AgentID: agent.ID, AgentName: agent.Name, AllowanceID: alw.ID, OrgID: org.ID,
		Signature: "5KdWbxNiZx5G42SeJrzQhcUTn9kVhzRoW4HwNXUT6Mxu",
		PayTo:     "Ex4Y68L2wRXsQ33x5oUz49KwjQ69ZCcwY99MsszehEdF",
		Host:      "api.exa.ai", Amount: money.Base(10_000),
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	e := store.HandshakeEvent{
		PaymentID: payID, Network: network.Sandbox,
		Phase: state.PhaseChallengeReceived, Writer: state.WriterSigner, At: now,
	}
	for i := 0; i < 3; i++ {
		if err := st.AppendPhase(ctx, e); err != nil {
			t.Fatalf("append %d: %v", i+1, err)
		}
	}
	tl, err := st.Timeline(ctx, payID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl) != 1 {
		t.Fatalf("the timeline has %d entries, want 1 — a phase happens once and is written once", len(tl))
	}
}
