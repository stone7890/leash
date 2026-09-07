package migrate

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// An owner's wallet IS their account.
//
// There is no password and no email address anywhere in the system, so there is nothing to phish
// and nothing to leak in a breach. The specification removed the `users` table from version 1 for
// this reason, and it stays removed: one owner, one workspace, one organisation.
//
// siws_nonces is here too, and it fills a gap the specification leaves. Sign-In With Solana is
// specified with no nonce store, which makes a signed sign-in message replayable for as long as it
// exists. A nonce is issued, consumed by DELETION — read-and-delete in one operation, so a replay
// finds nothing — and expires by TTL.
func init() {
	register(Migration{
		Version: 2,
		Name:    "an_owner_wallet_is_the_account",
		Up: func(ctx context.Context, db *mongo.Database) error {
			orgs := validator(schema(
				[]string{"_id", "owner_wallet", "plan", "active_network", "created_at"},
				bson.D{
					{Key: "_id", Value: pattern(idPattern("org"))},
					{Key: "owner_wallet", Value: pattern(base58)},
					{Key: "plan", Value: enum("free", "pro", "team")},
					// The network the dashboard is currently LOOKING at. It is a view preference
					// and nothing else: it never reaches a query, because every network-scoped
					// read takes its network from the record it is reading.
					{Key: "active_network", Value: enum("sandbox", "mainnet")},
					{Key: "workspace_name", Value: str(0, 64)},
					{Key: "alert_settings", Value: object()},
					{Key: "created_at", Value: date()},
				}))
			if err := createCollection(ctx, db, "orgs", orgs); err != nil {
				return err
			}
			if err := createIndexes(ctx, db, "orgs", []mongo.IndexModel{{
				Keys:    bson.D{{Key: "owner_wallet", Value: 1}},
				Options: options.Index().SetUnique(true).SetName("one_org_per_owner_wallet"),
			}}); err != nil {
				return err
			}

			nonces := validator(schema(
				[]string{"_id", "wallet", "issued_at", "expires_at"},
				bson.D{
					{Key: "_id", Value: str(16, 128)},
					{Key: "wallet", Value: pattern(base58)},
					{Key: "issued_at", Value: date()},
					{Key: "expires_at", Value: date()},
				}))
			if err := createCollection(ctx, db, "siws_nonces", nonces); err != nil {
				return err
			}
			return createIndexes(ctx, db, "siws_nonces", []mongo.IndexModel{{
				Keys: bson.D{{Key: "expires_at", Value: 1}},
				// A nonce that was never used still has to go. Deletion is the primary defence
				// against replay; the TTL is the sweeper behind it.
				Options: options.Index().SetExpireAfterSeconds(0).SetName("nonces_expire"),
			}})
		},
		Down: func(ctx context.Context, db *mongo.Database) error {
			if err := dropCollection(ctx, db, "siws_nonces"); err != nil {
				return err
			}
			return dropCollection(ctx, db, "orgs")
		},
	})
}
