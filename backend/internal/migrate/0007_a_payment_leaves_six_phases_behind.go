package migrate

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// A payment leaves six phases behind.
//
// The most important lines in this file are the two $cond clauses on handshake_events. They make
// invariant I4 a SCHEMA CONSTRAINT rather than a code review: a `confirmed` phase written by
// anything but the indexer, or without the slot it was read at, is refused by the server, in
// production, whichever runtime attempted it.
//
// `unknown` is a first-class payment state and is not a failure. It holds its money in the
// allowance's reservation and is re-checked for as long as it takes. Merging it into `failed` is
// the shortest path to paying twice for one challenge; merging it into `confirmed` is lying.
//
// Phase 4, `replayed`, has no writer. It happens inside the customer's agent runtime, which we do
// not run and cannot observe, so its row is simply ABSENT when it cannot be seen. Fabricating a
// timestamp for it would be inventing evidence in an audit trail.
func init() {
	register(Migration{
		Version: 7,
		Name:    "a_payment_leaves_six_phases_behind",
		Up: func(ctx context.Context, db *mongo.Database) error {
			payments := validator(
				schema(
					[]string{"_id", "sign_request_id", "agent_id", "allowance_id", "org_id",
						"network", "pay_to", "host", "amount_base", "state", "state_at", "created_at"},
					bson.D{
						{Key: "_id", Value: pattern(networkIDPattern("pay"))},
						{Key: "sign_request_id", Value: pattern(networkIDPattern("sgr"))},
						{Key: "agent_id", Value: pattern(networkIDPattern("agt"))},
						{Key: "allowance_id", Value: pattern(networkIDPattern("alw"))},
						{Key: "org_id", Value: pattern(idPattern("org"))},
						{Key: "network", Value: enum("sandbox", "mainnet")},
						{Key: "signature", Value: nullableStr()},
						// The transaction's replay nonce. When a facilitator sponsors the fee,
						// the chain knows the transaction by the FEE PAYER's signature — which
						// does not exist when we sign — so this is what the read-back matches on.
						{Key: "memo", Value: nullableStr()},
						// The signature the chain actually settled under, learned at read-back.
						{Key: "settled_signature", Value: nullableStr()},
						{Key: "pay_to", Value: pattern(base58)},
						{Key: "host", Value: str(1, 253)},
						// Denormalised, and immutable facts about THIS payment. The audit export is
						// a $lookup rather than a SQL join, and an unindexed join over months of
						// payments is slow — so the two fields a row is always read with live on it.
						{Key: "agent_name", Value: str(1, 64)},
						{Key: "amount_base", Value: amount(1)},
						{Key: "state", Value: enum("signed", "submitted", "confirmed", "failed", "unknown")},
						{Key: "state_at", Value: date()},
						{Key: "confirmed_slot", Value: nullableLong()},
						{Key: "last_checked_at", Value: nullableDate()},
						{Key: "created_at", Value: date()},
					}),
				and(
					networkMatchesID(),
					referenceIsSameNetwork("$agent_id"),
					referenceIsSameNetwork("$allowance_id"),
					referenceIsSameNetwork("$sign_request_id"),
					// I4: `confirmed` is a claim about the chain, and a claim about the chain
					// carries the slot it was read at.
					bson.D{{Key: "$cond", Value: bson.A{
						bson.D{{Key: "$eq", Value: bson.A{"$state", "confirmed"}}},
						and(
							bson.D{{Key: "$ne", Value: bson.A{
								bson.D{{Key: "$type", Value: "$confirmed_slot"}}, "missing"}}},
							bson.D{{Key: "$ne", Value: bson.A{"$confirmed_slot", nil}}},
							// Either signature will do: an unsponsored payment is known by the
							// agent's, a sponsored one by the fee payer's, learned at read-back.
							bson.D{{Key: "$or", Value: bson.A{
								bson.D{{Key: "$ne", Value: bson.A{
									bson.D{{Key: "$type", Value: "$signature"}}, "missing"}}},
								bson.D{{Key: "$ne", Value: bson.A{
									bson.D{{Key: "$type", Value: "$settled_signature"}}, "missing"}}},
							}}},
						),
						true,
					}}},
				),
			)
			if err := createCollection(ctx, db, "payments", payments); err != nil {
				return err
			}
			if err := createIndexes(ctx, db, "payments", []mongo.IndexModel{
				{
					// One challenge, one payment.
					Keys:    bson.D{{Key: "sign_request_id", Value: 1}},
					Options: options.Index().SetUnique(true).SetName("one_payment_per_challenge"),
				},
				{
					// Unique among the payments that HAVE a signature. PostgreSQL's UNIQUE already
					// ignores nulls; MongoDB needs the partial filter to reproduce that, or every
					// unsigned payment would collide with every other.
					Keys: bson.D{{Key: "signature", Value: 1}},
					Options: options.Index().SetUnique(true).SetName("one_payment_per_signature").
						SetPartialFilterExpression(bson.D{{Key: "signature",
							Value: bson.D{{Key: "$exists", Value: true}}}}),
				},
				{
					Keys:    bson.D{{Key: "agent_id", Value: 1}, {Key: "state_at", Value: -1}},
					Options: options.Index().SetName("payments_by_agent_newest_first"),
				},
				{
					// The sweep finds a sponsored payment by its memo.
					Keys: bson.D{{Key: "memo", Value: 1}},
					Options: options.Index().SetName("payments_by_memo").
						SetPartialFilterExpression(bson.D{{Key: "memo",
							Value: bson.D{{Key: "$exists", Value: true}}}}),
				},
				{
					// payment-sweep, per network: everything still in flight, oldest first.
					Keys: bson.D{{Key: "network", Value: 1}, {Key: "state", Value: 1},
						{Key: "state_at", Value: 1}},
					Options: options.Index().SetName("payments_still_in_flight"),
				},
				{
					// The audit export, streamed from a cursor rather than buffered.
					Keys:    bson.D{{Key: "org_id", Value: 1}, {Key: "state_at", Value: 1}},
					Options: options.Index().SetName("payments_for_export"),
				},
			}); err != nil {
				return err
			}

			handshake := validator(
				schema(
					[]string{"_id", "payment_id", "network", "phase", "writer", "at"},
					bson.D{
						{Key: "_id", Value: pattern(idPattern("hse"))},
						{Key: "payment_id", Value: pattern(networkIDPattern("pay"))},
						{Key: "network", Value: enum("sandbox", "mainnet")},
						{Key: "phase", Value: enum("challenge_received", "rules_evaluated",
							"signed", "replayed", "broadcast", "confirmed")},
						// Recorded rather than inferred, so a violation of I4 is visible IN THE
						// DATA and not only in a log nobody reads.
						{Key: "writer", Value: enum("signer", "indexer")},
						{Key: "read_back_slot", Value: nullableLong()},
						{Key: "detail", Value: object()},
						{Key: "detail_raw", Value: nullableStr()},
						{Key: "trace_id", Value: nullableStr()},
						{Key: "at", Value: date()},
					}),
				and(
					referenceIsSameNetwork("$payment_id"),
					// I4 as a constraint. `confirmed` comes from the indexer, and only with a slot.
					bson.D{{Key: "$cond", Value: bson.A{
						bson.D{{Key: "$eq", Value: bson.A{"$phase", "confirmed"}}},
						and(
							bson.D{{Key: "$eq", Value: bson.A{"$writer", "indexer"}}},
							bson.D{{Key: "$ne", Value: bson.A{
								bson.D{{Key: "$type", Value: "$read_back_slot"}}, "missing"}}},
							bson.D{{Key: "$ne", Value: bson.A{"$read_back_slot", nil}}},
						),
						true,
					}}},
					// Phases 1 to 3 are the signer's alone.
					bson.D{{Key: "$cond", Value: bson.A{
						bson.D{{Key: "$in", Value: bson.A{"$phase",
							bson.A{"challenge_received", "rules_evaluated", "signed"}}}},
						bson.D{{Key: "$eq", Value: bson.A{"$writer", "signer"}}},
						true,
					}}},
				),
			)
			if err := createCollection(ctx, db, "handshake_events", handshake); err != nil {
				return err
			}
			return createIndexes(ctx, db, "handshake_events", []mongo.IndexModel{
				{
					// This is what makes every job body safely re-runnable: a duplicate phase is
					// refused, so a dropped tick or a retried iteration needs no reasoning about.
					Keys:    bson.D{{Key: "payment_id", Value: 1}, {Key: "phase", Value: 1}},
					Options: options.Index().SetUnique(true).SetName("one_phase_per_payment"),
				},
				{
					Keys:    bson.D{{Key: "payment_id", Value: 1}, {Key: "at", Value: 1}},
					Options: options.Index().SetName("timeline_in_order"),
				},
			})
		},
		Down: func(ctx context.Context, db *mongo.Database) error {
			if err := dropCollection(ctx, db, "handshake_events"); err != nil {
				return err
			}
			return dropCollection(ctx, db, "payments")
		},
	})
}
