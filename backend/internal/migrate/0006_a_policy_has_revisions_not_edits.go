package migrate

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// A policy has revisions, not edits.
//
// `policies` is mutable and holds the current soft-tier rules. `policy_revisions` is APPEND-ONLY
// and holds how it got that way. Both are written in one transaction, so the audit row and the
// change land together or not at all — an audit trail with gaps at exactly the interesting moments
// is worse than none, because it is trusted.
//
// The one-host rule of the recovery flow lives partly here: `allow_hosts` is a list of exact
// hostnames, matched exactly, and the one-tap button adds precisely the host from the challenge.
// Matching loosely anywhere would quietly widen every allow-list that button has ever touched.
func init() {
	register(Migration{
		Version: 6,
		Name:    "a_policy_has_revisions_not_edits",
		Up: func(ctx context.Context, db *mongo.Database) error {
			policies := validator(schema(
				[]string{"_id", "allow_hosts", "allow_all", "per_tx_max_base",
					"velocity_max_base", "velocity_window_s", "updated_at"},
				bson.D{
					// One to one with the agent, so the agent's _id IS the key.
					{Key: "_id", Value: pattern(networkIDPattern("agt"))},
					{Key: "allow_hosts", Value: bson.D{
						{Key: "bsonType", Value: "array"},
						{Key: "maxItems", Value: 256},
						{Key: "items", Value: str(1, 253)},
					}},
					// Optional. Empty means any recipient at an allowed host, which is the
					// default. It exists because checking only the host is not enough: an allowed
					// endpoint that starts asking for payment to a different address is exactly
					// what a host-only check waves through.
					{Key: "allow_pay_to", Value: bson.D{
						{Key: "bsonType", Value: bson.A{"array", "null"}},
						{Key: "maxItems", Value: 256},
					}},
					// A wildcard is permitted, and the interface flags such an agent permanently.
					// That is the chosen trade between flexibility and honesty.
					{Key: "allow_all", Value: boolean()},
					{Key: "per_tx_max_base", Value: amount(1)},
					{Key: "velocity_max_base", Value: amount(1)},
					{Key: "velocity_window_s", Value: bson.D{
						{Key: "bsonType", Value: "int"},
						{Key: "minimum", Value: 1},
						{Key: "maximum", Value: 86400},
					}},
					{Key: "updated_at", Value: date()},
				}))
			if err := createCollection(ctx, db, "policies", policies); err != nil {
				return err
			}

			revisions := validator(schema(
				[]string{"_id", "agent_id", "change", "actor", "at"},
				bson.D{
					{Key: "_id", Value: pattern(idPattern("rev"))},
					{Key: "agent_id", Value: pattern(networkIDPattern("agt"))},
					{Key: "change", Value: bson.D{{Key: "bsonType", Value: "object"}}},
					{Key: "actor", Value: str(1, 128)},
					{Key: "reason", Value: nullableStr()},
					{Key: "at", Value: date()},
				}))
			if err := createCollection(ctx, db, "policy_revisions", revisions); err != nil {
				return err
			}
			if err := createIndexes(ctx, db, "policy_revisions", []mongo.IndexModel{{
				Keys:    bson.D{{Key: "agent_id", Value: 1}, {Key: "_id", Value: -1}},
				Options: options.Index().SetName("revisions_by_agent_newest_first"),
			}}); err != nil {
				return err
			}

			// The "This block is correct" button. It silences the ALERT and never the block, and
			// the interface says so — a customer who believes they whitelisted the host will
			// otherwise be confused by the next refusal.
			//
			// The composite document _id reproduces PRIMARY KEY (agent_id, host) exactly:
			// uniqueness, no second index, and a free upsert by key. Field order inside a document
			// _id is significant to MongoDB's equality, so it is constructed in one place and
			// never from a map.
			suppressions := validator(schema(
				[]string{"_id", "created_at"},
				bson.D{
					{Key: "_id", Value: schema([]string{"a", "h"}, bson.D{
						{Key: "a", Value: pattern(networkIDPattern("agt"))},
						{Key: "h", Value: str(1, 253)},
					})},
					{Key: "created_at", Value: date()},
				}))
			return createCollection(ctx, db, "suppressions", suppressions)
		},
		Down: func(ctx context.Context, db *mongo.Database) error {
			if err := dropCollection(ctx, db, "suppressions"); err != nil {
				return err
			}
			if err := dropCollection(ctx, db, "policy_revisions"); err != nil {
				return err
			}
			return dropCollection(ctx, db, "policies")
		},
	})
}
