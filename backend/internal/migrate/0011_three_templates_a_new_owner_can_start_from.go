package migrate

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Three templates a new owner can start from.
//
// The prefills come from the prototype and are the numbers a first-time owner sees before they
// have any judgement of their own about what an agent should be allowed to spend. They are
// deliberately small.
//
// Templates are seeded by a migration rather than by application code so that a fresh database is
// immediately usable, and read-only at runtime — the application role grants `find` and nothing
// else on this collection.
func init() {
	register(Migration{
		Version: 11,
		Name:    "three_templates_a_new_owner_can_start_from",
		Up: func(ctx context.Context, db *mongo.Database) error {
			templates := validator(schema(
				[]string{"_id", "name", "description", "prefills"},
				bson.D{
					// The specification's text primary key survives: these are seeded, not minted,
					// so they are the one kind with a readable identifier.
					{Key: "_id", Value: enum("research", "scraper", "blank")},
					{Key: "name", Value: str(1, 64)},
					{Key: "description", Value: str(1, 256)},
					{Key: "prefills", Value: bson.D{{Key: "bsonType", Value: "object"}}},
					{Key: "sort_order", Value: bson.D{{Key: "bsonType", Value: "int"}}},
				}))
			if err := createCollection(ctx, db, "templates", templates); err != nil {
				return err
			}

			now := time.Now().UTC()
			_ = now
			docs := []any{
				bson.D{
					{Key: "_id", Value: "research"},
					{Key: "name", Value: "Research agent"},
					{Key: "description", Value: "Search & content APIs. Small payments, tight per-call limit."},
					{Key: "sort_order", Value: int32(1)},
					{Key: "prefills", Value: bson.D{
						{Key: "cap_base", Value: int64(10_000_000)},     // $10.00
						{Key: "per_tx_max_base", Value: int64(250_000)}, // $0.25
						{Key: "velocity_max_base", Value: int64(1_000_000)},
						{Key: "velocity_window_s", Value: int32(600)},
						{Key: "expiry_days", Value: int32(7)},
						{Key: "allow_hosts", Value: bson.A{"api.exa.ai", "api.helius.dev"}},
					}},
				},
				bson.D{
					{Key: "_id", Value: "scraper"},
					{Key: "name", Value: "Scraper / browser"},
					{Key: "description", Value: "Browser sessions. Larger per-call, strict velocity."},
					{Key: "sort_order", Value: int32(2)},
					{Key: "prefills", Value: bson.D{
						{Key: "cap_base", Value: int64(50_000_000)},     // $50.00
						{Key: "per_tx_max_base", Value: int64(100_000)}, // $0.10
						{Key: "velocity_max_base", Value: int64(5_000_000)},
						{Key: "velocity_window_s", Value: int32(600)},
						{Key: "expiry_days", Value: int32(7)},
						{Key: "allow_hosts", Value: bson.A{"api.browserbase.com"}},
					}},
				},
				bson.D{
					{Key: "_id", Value: "blank"},
					{Key: "name", Value: "Start blank"},
					{Key: "description", Value: "No prefills. You set every rule yourself."},
					{Key: "sort_order", Value: int32(3)},
					{Key: "prefills", Value: bson.D{
						{Key: "cap_base", Value: int64(5_000_000)},     // $5.00
						{Key: "per_tx_max_base", Value: int64(50_000)}, // $0.05
						{Key: "velocity_max_base", Value: int64(500_000)},
						{Key: "velocity_window_s", Value: int32(600)},
						{Key: "expiry_days", Value: int32(7)},
						{Key: "allow_hosts", Value: bson.A{}},
					}},
				},
			}
			_, err := db.Collection("templates").InsertMany(ctx, docs)
			return err
		},
		Down: func(ctx context.Context, db *mongo.Database) error {
			return dropCollection(ctx, db, "templates")
		},
	})
}
