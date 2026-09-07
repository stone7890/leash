package store

import (
	"context"
	"errors"
	"time"

	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/domain/policy"
	"github.com/stone7890/leash/internal/domain/state"
	"github.com/stone7890/leash/internal/domain/tier"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ErrNotAdmitted is the compare-and-swap declining.
//
// It means S4 or S5, without saying which. In the ordinary case the caller already knows — it
// evaluated both against its snapshot before attempting the write. When the snapshot said allowed
// and the swap said no, that IS the race, and the loser re-reads and re-runs the pure engine to
// name the rule precisely. One extra round trip, on the rare losing path, is the right place to
// spend it.
var ErrNotAdmitted = errors.New("the allowance did not admit this amount")

// AdmitDraw is MongoDB's answer to SELECT ... FOR UPDATE, and it is the heart of the money path.
//
// The predicate is in the FILTER, so the read and the reservation are one atomic act on one
// document. Two requests racing for the last five cents do not both read "five cents remaining" —
// the loser's filter simply does not match, and it gets nothing back.
//
// S4 and S5 are decided TOGETHER because they are decided by the same document. Splitting them
// into two round trips would reintroduce the race between them.
//
// There is deliberately NO TRANSACTION here. A multi-document transaction aborts on write conflict
// and must be retried whole — and under this race, write conflict is not the exception, it is the
// case the design exists for. A transaction would turn the contended path into a retry storm and
// blow the 300ms budget in exactly the scenario that matters. A single-document findOneAndUpdate
// has the opposite property: the storage engine retries the conflict server-side, transparently.
//
// Belt and braces: even if this filter were wrong, the schema's `drawn + reserved <= cap` validator
// refuses the resulting document. You cannot over-admit through this collection.
func (s *Store) AdmitDraw(
	ctx context.Context,
	net network.Network,
	allowanceID string,
	amount money.Base,
	velocityMax money.Base,
	window time.Duration,
	now time.Time,
) (Allowance, error) {
	cutoff := now.Add(-window)

	// The velocity sum, over entries still inside the window. An entry exactly at the cutoff is
	// OUTSIDE it — $gte against the cutoff would include it, so this is $gt, matching the pure
	// engine's `e.At.After(cutoff)` exactly. The two are tested against each other.
	velocitySum := bson.D{{Key: "$sum", Value: bson.D{{Key: "$map", Value: bson.D{
		{Key: "input", Value: bson.D{{Key: "$filter", Value: bson.D{
			{Key: "input", Value: "$vel"},
			{Key: "as", Value: "v"},
			{Key: "cond", Value: bson.D{{Key: "$gt", Value: bson.A{"$$v.t", cutoff}}}},
		}}}},
		{Key: "as", Value: "v"},
		{Key: "in", Value: "$$v.a"},
	}}}}}

	conditions := bson.A{
		// S5 · remaining >= amount, where remaining is cap − drawn − reserved.
		bson.D{{Key: "$gte", Value: bson.A{
			bson.D{{Key: "$subtract", Value: bson.A{
				bson.D{{Key: "$subtract", Value: bson.A{"$cap_base", "$drawn_onchain_base"}}},
				"$reserved_base",
			}}},
			int64(amount),
		}}},
	}
	// S4 · velocity, only when the agent has a window configured.
	if velocityMax > 0 && window > 0 {
		conditions = append(conditions, bson.D{{Key: "$lte", Value: bson.A{
			bson.D{{Key: "$add", Value: bson.A{int64(amount), velocitySum}}},
			int64(velocityMax),
		}}})
	}

	filter := bson.D{
		{Key: "_id", Value: allowanceID},
		// I8: the network is a parameter, and it is in the filter. There is no overload of this
		// method that does not take one.
		{Key: "network", Value: net.String()},
		{Key: "state", Value: string(state.AllowanceActive)},
		{Key: "$expr", Value: bson.D{{Key: "$and", Value: conditions}}},
	}

	// An update PIPELINE, so the window is pruned in the same atomic act it is appended to.
	// Otherwise entries older than ten minutes accumulate until the document hits its size limit.
	update := mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "reserved_base", Value: bson.D{{Key: "$add", Value: bson.A{
			"$reserved_base", int64(amount)}}}},
		{Key: "vel", Value: bson.D{{Key: "$concatArrays", Value: bson.A{
			bson.D{{Key: "$filter", Value: bson.D{
				{Key: "input", Value: "$vel"},
				{Key: "as", Value: "v"},
				{Key: "cond", Value: bson.D{{Key: "$gt", Value: bson.A{"$$v.t", cutoff}}}},
			}}},
			bson.A{bson.D{{Key: "t", Value: now}, {Key: "a", Value: int64(amount)}}},
		}}}},
	}}}}

	var doc allowanceDoc
	err := s.db.Collection("allowances").FindOneAndUpdate(ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Allowance{}, ErrNotAdmitted
	}
	if err != nil {
		return Allowance{}, classify(err)
	}
	return doc.toAllowance(), nil
}

// ReleaseReservation gives budget back when a payment turns out not to have moved.
//
// It is called only from the payment sweep, and only for a payment that has been READ BACK as
// failed. A reservation released on a guess is budget handed to the next payment for money that
// may still move.
func (s *Store) ReleaseReservation(ctx context.Context, net network.Network,
	allowanceID string, amount money.Base) error {
	_, err := s.db.Collection("allowances").UpdateOne(ctx,
		bson.D{
			{Key: "_id", Value: allowanceID},
			{Key: "network", Value: net.String()},
			// Never below zero. The validator would refuse it anyway; this makes the intent local.
			{Key: "reserved_base", Value: bson.D{{Key: "$gte", Value: int64(amount)}}},
		},
		bson.D{{Key: "$inc", Value: bson.D{{Key: "reserved_base", Value: -int64(amount)}}}})
	return classify(err)
}

// ConfirmDraw is the ONLY place `reserved` is decremented alongside `drawn` being raised.
//
// The way to get this wrong is to release the reservation when a payment confirms, before the
// chain has been re-read. For a moment the money would be in neither term, and the signer would
// over-admit. So both happen in one atomic update, from a chain read whose slot is at least the
// payment's confirmation slot — which makes every window CONSERVATIVE: the money is counted twice,
// never zero times. That under-reports the budget and never overspends it.
func (s *Store) ConfirmDraw(ctx context.Context, net network.Network,
	allowanceID string, amount money.Base, drawnOnChain money.Base, slot int64, now time.Time) error {

	// An update pipeline, because the reservation must be decremented and the chain figures written
	// in ONE act. Two updates would leave a window in which the money is in neither term.
	//
	// $max against zero rather than a bare subtraction: if a reservation has already been released
	// by another path, going negative would be refused by the validator and the confirmation would
	// be lost. Clamping keeps the read-back — which is the thing we must not drop — while the
	// 60-second recomputation repairs the number and reports the drift.
	update := mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "drawn_onchain_base", Value: int64(drawnOnChain)},
		{Key: "reserved_base", Value: bson.D{{Key: "$max", Value: bson.A{
			int64(0),
			bson.D{{Key: "$subtract", Value: bson.A{"$reserved_base", int64(amount)}}},
		}}}},
		{Key: "last_read_slot", Value: slot},
		{Key: "last_read_at", Value: now},
	}}}}

	_, err := s.db.Collection("allowances").UpdateOne(ctx,
		bson.D{
			{Key: "_id", Value: allowanceID},
			{Key: "network", Value: net.String()},
			// Never move backwards. A slower read-back arriving after a faster one must not undo
			// it — the chain only moves forward, and so does what we believe about it.
			{Key: "$or", Value: bson.A{
				bson.D{{Key: "last_read_slot", Value: bson.D{{Key: "$lte", Value: slot}}}},
				bson.D{{Key: "last_read_slot", Value: bson.D{{Key: "$exists", Value: false}}}},
			}},
		},
		update)
	return classify(err)
}

// RefreshFromChain writes what a read-back found, and is the only path from pending to active.
//
// The schema refuses an active allowance with no slot, so this method is structurally the only way
// an allowance can become spendable. Invariant I4, expressed as "there is no other function".
func (s *Store) RefreshFromChain(ctx context.Context, net network.Network, allowanceID string,
	drawn money.Base, st state.Allowance, slot int64, now time.Time) error {
	set := bson.D{
		{Key: "drawn_onchain_base", Value: int64(drawn)},
		{Key: "last_read_slot", Value: slot},
		{Key: "last_read_at", Value: now},
		{Key: "state", Value: string(st)},
		{Key: "state_at", Value: now},
	}
	update := bson.D{{Key: "$set", Value: set}}
	// Leaving the live states removes the materialised key in the SAME update that changes the
	// state, so the index and the state can never disagree.
	if !st.Live() {
		update = append(update, bson.E{Key: "$unset", Value: bson.D{{Key: "open_key", Value: ""}}})
	}
	_, err := s.db.Collection("allowances").UpdateOne(ctx,
		bson.D{{Key: "_id", Value: allowanceID}, {Key: "network", Value: net.String()}},
		update)
	return classify(err)
}

// RecomputeReserved sums the payments still in flight and reports the drift.
//
// This is the compensating control for the one thing MongoDB made worse: `reserved` is a STORED
// derived value, where PostgreSQL would have computed it live inside a row lock and it could not
// drift. Drift is a bug — either a payment refused that should have gone through, or one admitted
// that should not — so it is repaired every 60 seconds and the delta is logged.
func (s *Store) RecomputeReserved(ctx context.Context, net network.Network, allowanceID string) (
	stored money.Base, actual money.Base, err error) {

	var doc allowanceDoc
	if err := s.db.Collection("allowances").FindOne(ctx,
		bson.D{{Key: "_id", Value: allowanceID}, {Key: "network", Value: net.String()}},
	).Decode(&doc); err != nil {
		return 0, 0, classify(err)
	}

	cur, err := s.db.Collection("payments").Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.D{
			{Key: "allowance_id", Value: allowanceID},
			{Key: "network", Value: net.String()},
			// `unknown` is in the in-flight set. A payment whose outcome we have not observed
			// still holds its money — assuming it failed is how one challenge gets paid twice.
			{Key: "state", Value: bson.D{{Key: "$in", Value: bson.A{
				string(state.PaymentSigned),
				string(state.PaymentSubmitted),
				string(state.PaymentUnknown),
			}}}},
		}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: nil},
			{Key: "total", Value: bson.D{{Key: "$sum", Value: "$amount_base"}}},
		}}},
	})
	if err != nil {
		return 0, 0, classify(err)
	}
	defer cur.Close(ctx)

	var out []struct {
		Total int64 `bson:"total"`
	}
	if err := cur.All(ctx, &out); err != nil {
		return 0, 0, classify(err)
	}
	var total int64
	if len(out) > 0 {
		total = out[0].Total
	}
	return doc.Reserved, money.Base(total), nil
}

// SetReserved repairs drift found by RecomputeReserved.
func (s *Store) SetReserved(ctx context.Context, net network.Network,
	allowanceID string, reserved money.Base) error {
	_, err := s.db.Collection("allowances").UpdateOne(ctx,
		bson.D{{Key: "_id", Value: allowanceID}, {Key: "network", Value: net.String()}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "reserved_base", Value: int64(reserved)}}}})
	return classify(err)
}

// allowanceDoc is the stored shape. It is unexported: the driver's types stop here.
type allowanceDoc struct {
	ID           string     `bson:"_id"`
	AgentID      string     `bson:"agent_id"`
	Network      string     `bson:"network"`
	OnchainAddr  string     `bson:"onchain_addr"`
	Mint         string     `bson:"mint"`
	TokenProgram string     `bson:"token_program_id,omitempty"`
	Cap          money.Base `bson:"cap_base"`
	Drawn        money.Base `bson:"drawn_onchain_base"`
	Reserved     money.Base `bson:"reserved_base"`
	LastReadSlot *int64     `bson:"last_read_slot,omitempty"`
	LastReadAt   *time.Time `bson:"last_read_at,omitempty"`
	ExpiryTS     *time.Time `bson:"expiry_ts,omitempty"`
	ExpiryTier   string     `bson:"expiry_tier"`
	State        string     `bson:"state"`
	StateAt      time.Time  `bson:"state_at"`
	Epoch        int32      `bson:"epoch"`
	OpenKey      string     `bson:"open_key,omitempty"`
	Vel          []velEntry `bson:"vel"`
	CreatedAt    time.Time  `bson:"created_at"`
}

type velEntry struct {
	At     time.Time  `bson:"t"`
	Amount money.Base `bson:"a"`
}

func (d allowanceDoc) toAllowance() Allowance {
	a := Allowance{
		ID: d.ID, AgentID: d.AgentID, Network: network.Network(d.Network),
		OnchainAddr: d.OnchainAddr, Mint: d.Mint, TokenProgram: d.TokenProgram,
		Cap: d.Cap, Drawn: d.Drawn, Reserved: d.Reserved,
		ExpiryTier: tier.Tier(d.ExpiryTier), State: state.Allowance(d.State),
		StateAt: d.StateAt, Epoch: int(d.Epoch), CreatedAt: d.CreatedAt,
	}
	if d.LastReadSlot != nil {
		a.LastReadSlot = *d.LastReadSlot
	}
	if d.LastReadAt != nil {
		a.LastReadAt = *d.LastReadAt
	}
	if d.ExpiryTS != nil {
		a.ExpiryTS = *d.ExpiryTS
	}
	return a
}

func (d allowanceDoc) velocity() []policy.VelocityEntry {
	out := make([]policy.VelocityEntry, 0, len(d.Vel))
	for _, v := range d.Vel {
		out = append(out, policy.VelocityEntry{At: v.At, Amount: v.Amount})
	}
	return out
}
