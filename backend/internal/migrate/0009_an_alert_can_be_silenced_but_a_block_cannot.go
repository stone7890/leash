package migrate

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// An alert can be silenced, but a block cannot.
//
// The distinction is the whole of the recovery flow. "Add this host to the allow list" changes
// what happens; "This block is correct" changes only what you hear about. The suppressions
// collection in migration 6 carries the second, and the interface is required to say plainly that
// the blocking continues — a customer who believes they whitelisted the host will otherwise be
// confused by the next refusal.
func init() {
	register(Migration{
		Version: 9,
		Name:    "an_alert_can_be_silenced_but_a_block_cannot",
		Up: func(ctx context.Context, db *mongo.Database) error {
			alerts := validator(schema(
				[]string{"_id", "org_id", "kind", "body", "created_at"},
				bson.D{
					{Key: "_id", Value: pattern(idPattern("alr"))},
					{Key: "org_id", Value: pattern(idPattern("org"))},
					{Key: "agent_id", Value: nullableStr()},
					{Key: "network", Value: enum("sandbox", "mainnet")},
					{Key: "kind", Value: enum("budget_warning", "budget_critical", "blocked",
						"new_endpoint", "expiring_soon", "payment_failed")},
					{Key: "body", Value: bson.D{{Key: "bsonType", Value: "object"}}},
					// Delivery is recorded separately from creation: an alert we generated but
					// could not deliver is a different situation from one we never generated, and
					// merging them would hide a broken webhook.
					{Key: "sent_at", Value: nullableDate()},
					{Key: "delivery", Value: object()},
					// The dedupe key. alert-eval runs every 30 seconds, and a burn-rate threshold
					// stays crossed — without this an owner would be told once per tick.
					{Key: "dedupe_key", Value: str(1, 256)},
					{Key: "created_at", Value: date()},
				}))
			if err := createCollection(ctx, db, "alerts", alerts); err != nil {
				return err
			}
			return createIndexes(ctx, db, "alerts", []mongo.IndexModel{
				{
					Keys:    bson.D{{Key: "org_id", Value: 1}, {Key: "created_at", Value: -1}},
					Options: options.Index().SetName("alerts_newest_first"),
				},
				{
					// One alert per situation, not one per evaluation.
					Keys:    bson.D{{Key: "dedupe_key", Value: 1}},
					Options: options.Index().SetUnique(true).SetName("one_alert_per_situation"),
				},
			})
		},
		Down: func(ctx context.Context, db *mongo.Database) error {
			return dropCollection(ctx, db, "alerts")
		},
	})
}
