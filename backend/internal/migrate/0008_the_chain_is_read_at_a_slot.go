package migrate

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// The chain is read at a slot, and we record where we got to.
//
// Invariant I2 says the database is a cache carrying the slot it was read at. This migration adds
// the per-network cursor the indexer keeps, so a restart resumes rather than re-reading, and so
// the interface's slot chip and degraded banner have a number to show.
//
// One document per network, keyed by the network itself. There is deliberately no global cursor:
// a single one would be the shared piece of state capable of letting a sandbox read advance a
// mainnet position.
func init() {
	register(Migration{
		Version: 8,
		Name:    "the_chain_is_read_at_a_slot",
		Up: func(ctx context.Context, db *mongo.Database) error {
			cursors := validator(schema(
				[]string{"_id", "last_slot", "last_read_at"},
				bson.D{
					{Key: "_id", Value: enum("sandbox", "mainnet")},
					{Key: "last_slot", Value: bson.D{
						{Key: "bsonType", Value: "long"}, {Key: "minimum", Value: 0}}},
					{Key: "last_read_at", Value: date()},
					// Which endpoint answered. When the primary fails and the fallback takes over,
					// the interface says so rather than hiding it — a degraded mode that is
					// invisible is a degraded mode nobody fixes.
					{Key: "using", Value: enum("primary", "fallback")},
					{Key: "lag_seconds", Value: bson.D{
						{Key: "bsonType", Value: "int"}, {Key: "minimum", Value: 0}}},
				}))
			return createCollection(ctx, db, "chain_cursors", cursors)
		},
		Down: func(ctx context.Context, db *mongo.Database) error {
			return dropCollection(ctx, db, "chain_cursors")
		},
	})
}
