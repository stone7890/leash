package migrate

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// A challenge is signed exactly once.
//
// Invariant I7, enforced at storage rather than in code, because two signer processes — or one
// behind a retrying load balancer — defeat an application check the first time they race, and a
// unique index does not race.
//
// TWO collections, and the split matters. `sign_requests` is APPEND-ONLY and carries a verdict,
// which is not known at the moment a request must win the race. `sign_claims` does the claiming:
// its _id is the challenge hash, and inserting it is how a request wins. Trying to make one
// document be both the race token and the immutable verdict record is where this design goes wrong.
//
// `sign_claims` also carries the pinned blockhash. Ed25519 is deterministic, so with the blockhash
// fixed, re-signing the same challenge after a crash produces BYTE-IDENTICAL output. That is what
// lets the signing path run without a transaction and still never hand out two signatures: there
// is one signature, produced twice.
func init() {
	register(Migration{
		Version: 4,
		Name:    "a_challenge_is_signed_exactly_once",
		Up: func(ctx context.Context, db *mongo.Database) error {
			claims := validator(schema(
				[]string{"_id", "agent_id", "network", "state", "created_at"},
				bson.D{
					// The challenge hash. sha256 over the canonical form, hex encoded.
					{Key: "_id", Value: str(64, 64)},
					{Key: "agent_id", Value: pattern(networkIDPattern("agt"))},
					{Key: "network", Value: enum("sandbox", "mainnet")},
					{Key: "state", Value: enum("in_flight", "done")},
					// Pinned so a retry after a crash rebuilds the identical message.
					{Key: "blockhash", Value: nullableStr()},
					// The stored answer, replayed verbatim to a retry (I7).
					{Key: "response", Value: object()},
					{Key: "sign_request_id", Value: nullableStr()},
					{Key: "created_at", Value: date()},
					{Key: "settled_at", Value: nullableDate()},
				}))
			if err := createCollection(ctx, db, "sign_claims", claims); err != nil {
				return err
			}
			if err := createIndexes(ctx, db, "sign_claims", []mongo.IndexModel{{
				// The stale-claim sweeper: a claim in flight with no sign request behind it is a
				// crash between claiming and recording, and it is released after 60 seconds.
				Keys:    bson.D{{Key: "state", Value: 1}, {Key: "created_at", Value: 1}},
				Options: options.Index().SetName("claims_to_sweep"),
			}}); err != nil {
				return err
			}

			signRequests := validator(
				schema(
					[]string{"_id", "agent_id", "network", "challenge_hash", "verdict",
						"checks", "created_at"},
					bson.D{
						{Key: "_id", Value: pattern(networkIDPattern("sgr"))},
						{Key: "agent_id", Value: pattern(networkIDPattern("agt"))},
						{Key: "network", Value: enum("sandbox", "mainnet")},
						{Key: "challenge_hash", Value: str(64, 64)},
						{Key: "verdict", Value: enum("allowed", "blocked")},
						{Key: "failed_rule", Value: nullableStr()},
						// One entry per rule, in evaluation order. This is the authoritative record
						// of what was checked, and the interface renders "N of N" from its length
						// rather than from a literal — which is how the specification's own
						// disagreement about the count (six, seven, or the eight that exist)
						// stops being able to recur.
						{Key: "checks", Value: bson.D{{Key: "bsonType", Value: "array"}}},
						{Key: "requirements", Value: object()},
						// The canonical text the hash was taken over. Storing it is storing the
						// thing that was hashed, which is what makes a disputed payment
						// reconcilable.
						{Key: "requirements_raw", Value: nullableStr()},
						{Key: "warnings", Value: bson.D{{Key: "bsonType", Value: bson.A{"array", "null"}}}},
						{Key: "created_at", Value: date()},
					}),
				and(
					networkMatchesID(),
					referenceIsSameNetwork("$agent_id"),
					// A block must name the rule that caused it; an allow must not claim one.
					bson.D{{Key: "$cond", Value: bson.A{
						bson.D{{Key: "$eq", Value: bson.A{"$verdict", "blocked"}}},
						bson.D{{Key: "$ne", Value: bson.A{
							bson.D{{Key: "$type", Value: "$failed_rule"}}, "missing"}}},
						bson.D{{Key: "$in", Value: bson.A{
							bson.D{{Key: "$type", Value: "$failed_rule"}},
							bson.A{"missing", "null"}}}},
					}}},
				),
			)
			if err := createCollection(ctx, db, "sign_requests", signRequests); err != nil {
				return err
			}
			return createIndexes(ctx, db, "sign_requests", []mongo.IndexModel{
				{
					// The second, independent guard on I7. The claim decides the race; this makes
					// a duplicate record impossible even if the claim logic were wrong.
					Keys:    bson.D{{Key: "challenge_hash", Value: 1}},
					Options: options.Index().SetUnique(true).SetName("one_sign_request_per_challenge"),
				},
				{
					// FIFO is reconstructed from this: the order requests reached the allowance
					// compare-and-swap, recoverable from the monotonic ULID _id.
					Keys:    bson.D{{Key: "agent_id", Value: 1}, {Key: "_id", Value: -1}},
					Options: options.Index().SetName("sign_requests_by_agent_in_order"),
				},
			})
		},
		Down: func(ctx context.Context, db *mongo.Database) error {
			if err := dropCollection(ctx, db, "sign_requests"); err != nil {
				return err
			}
			return dropCollection(ctx, db, "sign_claims")
		},
	})
}
