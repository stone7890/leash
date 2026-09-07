package migrate

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// The faucet gives once an hour, per wallet.
//
// The sandbox has no other limits by design — it is play money and the whole point is that a
// developer can spend it freely. The faucet is the one part of it that costs us something, so it
// is the one part that is metered.
//
// The limit lives in the database rather than in a process's memory for two reasons: it must
// survive a restart, and it must be shared between instances. An in-memory counter is neither, and
// the failure is silent — the limit simply stops applying.
func init() {
	register(Migration{
		Version: 12,
		Name:    "the_faucet_gives_once_an_hour",
		Up: func(ctx context.Context, db *mongo.Database) error {
			grants := validator(schema(
				[]string{"_id", "wallet", "network", "granted_at", "expires_at"},
				bson.D{
					{Key: "_id", Value: pattern(idPattern("job"))},
					{Key: "wallet", Value: pattern(base58)},
					// Sandbox only. There is no mainnet faucet and there never will be, so the
					// enum has one value rather than two and a mainnet row cannot be written.
					{Key: "network", Value: enum("sandbox")},
					{Key: "sol_lamports", Value: bson.D{{Key: "bsonType", Value: "long"}}},
					{Key: "usdc_base", Value: amount(1)},
					{Key: "granted_at", Value: date()},
					{Key: "expires_at", Value: date()},
				}))
			if err := createCollection(ctx, db, "faucet_grants", grants); err != nil {
				return err
			}
			return createIndexes(ctx, db, "faucet_grants", []mongo.IndexModel{
				{
					Keys:    bson.D{{Key: "wallet", Value: 1}, {Key: "granted_at", Value: -1}},
					Options: options.Index().SetName("grants_by_wallet"),
				},
				{
					// The rows are a rate limit, not a record worth keeping. They expire on their
					// own so the collection cannot grow without bound.
					Keys:    bson.D{{Key: "expires_at", Value: 1}},
					Options: options.Index().SetExpireAfterSeconds(0).SetName("grants_expire"),
				},
			})
		},
		Down: func(ctx context.Context, db *mongo.Database) error {
			return dropCollection(ctx, db, "faucet_grants")
		},
	})
}
