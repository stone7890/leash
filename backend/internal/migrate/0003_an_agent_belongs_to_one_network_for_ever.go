package migrate

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// An agent belongs to one network, for ever.
//
// This is invariant I8, and it is the migration where it becomes structural rather than
// aspirational. The specification asks for a trigger forbidding UPDATE on the network column. We
// do something stronger: the network is a COMPONENT OF _id — agt_test_… and agt_live_… — and
// MongoDB refuses any update that modifies _id at the storage engine. The $expr below then pins
// the redundant `network` field to that prefix, so the two cannot drift apart.
//
// The result: updateOne({$set:{network:"mainnet"}}) on an agt_test_… document fails with
// DocumentFailedValidation, and there is no aggregation-pipeline update, no $rename, and no raw
// write that gets around it. It is also visible to the naked eye in a URL, a log line and a
// support ticket, which a trigger would not have been.
//
// agent_keys is created here and owned by the SIGNER alone. Its _id IS the agent's, because the
// relationship is one to one — so the reference cannot dangle and no second index is needed.
func init() {
	register(Migration{
		Version: 3,
		Name:    "an_agent_belongs_to_one_network_for_ever",
		Up: func(ctx context.Context, db *mongo.Database) error {
			agents := validator(
				schema(
					[]string{"_id", "org_id", "name", "template_id", "network", "pubkey",
						"idempotency_key", "killed", "created_at"},
					bson.D{
						{Key: "_id", Value: pattern(networkIDPattern("agt"))},
						{Key: "org_id", Value: pattern(idPattern("org"))},
						{Key: "name", Value: str(1, 64)},
						{Key: "template_id", Value: enum("research", "scraper", "blank")},
						{Key: "network", Value: enum("sandbox", "mainnet")},
						{Key: "runs_as", Value: enum("mcp", "cli", "hosted")},
						{Key: "pubkey", Value: pattern(base58)},
						{Key: "idempotency_key", Value: str(8, 128)},
						{Key: "killed", Value: boolean()},
						{Key: "killed_at", Value: nullableDate()},
						{Key: "created_at", Value: date()},
					}),
				networkMatchesID(),
			)
			if err := createCollection(ctx, db, "agents", agents); err != nil {
				return err
			}
			if err := createIndexes(ctx, db, "agents", []mongo.IndexModel{
				{
					Keys:    bson.D{{Key: "pubkey", Value: 1}},
					Options: options.Index().SetUnique(true).SetName("one_agent_per_pubkey"),
				},
				{
					// I7 inward: a retried create returns the original agent rather than making a
					// second one. Enforced by the index, not by an application check — two
					// instances behind a load balancer defeat an application check the first time
					// they race, and an index does not race.
					Keys:    bson.D{{Key: "idempotency_key", Value: 1}},
					Options: options.Index().SetUnique(true).SetName("one_agent_per_idempotency_key"),
				},
				{
					Keys: bson.D{{Key: "org_id", Value: 1}, {Key: "network", Value: 1},
						{Key: "created_at", Value: -1}},
					Options: options.Index().SetName("agents_by_org_and_network"),
				},
			}); err != nil {
				return err
			}

			keys := validator(schema(
				[]string{"_id", "kms_key_ref", "wrapped_key", "created_at"},
				bson.D{
					{Key: "_id", Value: pattern(networkIDPattern("agt"))},
					{Key: "kms_key_ref", Value: str(1, 512)},
					// The agent's Ed25519 seed, encrypted with a data key that is itself wrapped by
					// KMS. The database never holds a usable key, and KMS never sees the seed.
					{Key: "wrapped_key", Value: bson.D{{Key: "bsonType", Value: "binData"}}},
					{Key: "key_hash", Value: str(64, 64)},
					{Key: "rotated_at", Value: nullableDate()},
					{Key: "created_at", Value: date()},
				}))
			if err := createCollection(ctx, db, "agent_keys", keys); err != nil {
				return err
			}
			return createIndexes(ctx, db, "agent_keys", []mongo.IndexModel{{
				// The signer looks an agent up by the hash of the presented API key. Unique,
				// because a rotation must invalidate the old key immediately — there is no
				// overlap window, since a rotation is usually a response to a suspected leak.
				Keys:    bson.D{{Key: "key_hash", Value: 1}},
				Options: options.Index().SetUnique(true).SetName("one_agent_per_api_key"),
			}})
		},
		Down: func(ctx context.Context, db *mongo.Database) error {
			if err := dropCollection(ctx, db, "agent_keys"); err != nil {
				return err
			}
			return dropCollection(ctx, db, "agents")
		},
	})
}
