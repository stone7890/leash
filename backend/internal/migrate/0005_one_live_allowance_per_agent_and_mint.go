package migrate

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// An agent holds exactly one allowance per mint that can still spend.
//
// This file carries more of the system's correctness than any other. Three things in it are
// load-bearing.
//
// ONE. `drawn + reserved <= cap` is the budget invariant itself, enforced by the database on every
// write. The signer's admission filter cannot over-admit even if the filter is wrong, because the
// resulting document would be refused. Under validationLevel "strict" this runs on the $inc too.
//
// TWO. `open_key` is a MATERIALISED key rather than a partial filter over `state`. The literal
// translation of `UNIQUE (agent_id, mint) WHERE state IN ('pending','active')` is a
// partialFilterExpression with $in, which works on current servers — but that filter historically
// accepted only $eq, $exists, the comparison operators, $type and $and. A schema whose correctness
// depends on remembering which server version added which operator is a schema that will be wrong
// on somebody's machine. `open_key` needs only $exists, which every version has, and the $expr
// below makes the field and the state incapable of disagreeing.
//
// THREE. `expiry_tier` is stored, not assumed. The shipping SPL adapter enforces a cap and a
// revocation on-chain but has no expiry, so expiry is a signer-tier rule today and would become an
// on-chain one if the Subscriptions & Allowances primitive ships. Invariant I3 forbids the
// interface from claiming a tier the adapter does not deliver, so the tier is data and the
// interface renders from it.
func init() {
	register(Migration{
		Version: 5,
		Name:    "one_live_allowance_per_agent_and_mint",
		Up: func(ctx context.Context, db *mongo.Database) error {
			allowances := validator(
				schema(
					[]string{"_id", "agent_id", "network", "onchain_addr", "mint", "cap_base",
						"drawn_onchain_base", "reserved_base", "state", "state_at", "epoch",
						"expiry_tier", "vel", "created_at"},
					bson.D{
						{Key: "_id", Value: pattern(networkIDPattern("alw"))},
						{Key: "agent_id", Value: pattern(networkIDPattern("agt"))},
						{Key: "network", Value: enum("sandbox", "mainnet")},
						{Key: "onchain_addr", Value: pattern(base58)},
						{Key: "mint", Value: pattern(base58)},
						// The classic SPL program, or Token-2022. Using the wrong one produces a
						// confusing failure rather than an obvious one, so it is carried per mint.
						{Key: "token_program_id", Value: pattern(base58)},

						{Key: "cap_base", Value: amount(1)},
						{Key: "drawn_onchain_base", Value: amount(0)},
						// The in-flight total. PostgreSQL would not have needed this — a
						// SELECT ... FOR UPDATE computes it live and it cannot drift because it is
						// never stored. It is materialised here because MongoDB has no row lock,
						// and that is a real deviation: see docs/16-deck-conformance.md §B-3.
						{Key: "reserved_base", Value: amount(0)},

						// I2: any number the interface shows must be able to name the slot it was
						// read at.
						{Key: "last_read_slot", Value: nullableLong()},
						{Key: "last_read_at", Value: nullableDate()},

						{Key: "expiry_ts", Value: nullableDate()},
						{Key: "expiry_tier", Value: enum("onchain", "signer")},

						{Key: "state", Value: enum("pending", "active", "exhausted", "expired", "revoked")},
						{Key: "state_at", Value: date()},
						// SPL `approve` REPLACES the delegated amount rather than adding to it, so
						// a top-up starts a new (cap, drawn = 0) generation. Without this, the
						// amount already spent would count against the new cap.
						{Key: "epoch", Value: bson.D{{Key: "bsonType", Value: "int"}, {Key: "minimum", Value: 1}}},

						{Key: "open_key", Value: bson.D{{Key: "bsonType", Value: "string"}}},

						// The velocity window lives on the same document as the budget so that ONE
						// atomic update can decide S4 and S5 together. An in-memory window would be
						// correct for a single signer process and silently wrong the day there are
						// two.
						{Key: "vel", Value: bson.D{
							{Key: "bsonType", Value: "array"},
							{Key: "maxItems", Value: 512},
							{Key: "items", Value: schema([]string{"t", "a"}, bson.D{
								{Key: "t", Value: date()},
								{Key: "a", Value: amount(1)},
							})},
						}},
						{Key: "created_at", Value: date()},
					}),

				and(
					// I8, both ways: this allowance's own network, and the agent it points at.
					networkMatchesID(),
					referenceIsSameNetwork("$agent_id"),

					// The specification's CHECK (drawn_onchain_base BETWEEN 0 AND cap_base) …
					bson.D{{Key: "$lte", Value: bson.A{"$drawn_onchain_base", "$cap_base"}}},

					// … and the invariant it was standing in for. THIS is the line that makes
					// over-admission impossible rather than merely unlikely.
					bson.D{{Key: "$lte", Value: bson.A{
						bson.D{{Key: "$add", Value: bson.A{"$drawn_onchain_base", "$reserved_base"}}},
						"$cap_base",
					}}},

					// The materialised key stays honest: present and correct exactly while the
					// allowance can still spend, absent otherwise.
					bson.D{{Key: "$cond", Value: bson.A{
						bson.D{{Key: "$in", Value: bson.A{"$state", bson.A{"pending", "active"}}}},
						bson.D{{Key: "$eq", Value: bson.A{"$open_key",
							bson.D{{Key: "$concat", Value: bson.A{"$agent_id", "|", "$mint"}}}}}},
						bson.D{{Key: "$eq", Value: bson.A{
							bson.D{{Key: "$type", Value: "$open_key"}}, "missing"}}},
					}}},

					// I4: `active` is a claim about the chain, so it needs a slot behind it. There
					// is no path from `pending` to `active` that does not go through a read-back.
					bson.D{{Key: "$cond", Value: bson.A{
						bson.D{{Key: "$eq", Value: bson.A{"$state", "active"}}},
						bson.D{{Key: "$ne", Value: bson.A{
							bson.D{{Key: "$type", Value: "$last_read_slot"}}, "missing"}}},
						true,
					}}},
				),
			)
			if err := createCollection(ctx, db, "allowances", allowances); err != nil {
				return err
			}
			return createIndexes(ctx, db, "allowances", []mongo.IndexModel{
				{
					Keys:    bson.D{{Key: "onchain_addr", Value: 1}},
					Options: options.Index().SetUnique(true).SetName("one_allowance_per_onchain_account"),
				},
				{
					// Two live allowances for one agent and mint would mean two budgets counting
					// the same on-chain delegation — and it goes wrong only under concurrency,
					// which is to say, at the demo.
					Keys: bson.D{{Key: "open_key", Value: 1}},
					Options: options.Index().SetUnique(true).SetName("one_active_allowance").
						SetPartialFilterExpression(bson.D{{Key: "open_key",
							Value: bson.D{{Key: "$exists", Value: true}}}}),
				},
				{
					Keys:    bson.D{{Key: "agent_id", Value: 1}, {Key: "state", Value: 1}},
					Options: options.Index().SetName("allowances_by_agent"),
				},
				{
					// allowance-refresh, per network, every 60 seconds.
					Keys:    bson.D{{Key: "network", Value: 1}, {Key: "last_read_at", Value: 1}},
					Options: options.Index().SetName("allowances_due_for_refresh"),
				},
			})
		},
		Down: func(ctx context.Context, db *mongo.Database) error {
			return dropCollection(ctx, db, "allowances")
		},
	})
}
