package migrate

import (
	"github.com/stone7890/leash/internal/domain/money"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// Shared schema fragments.
//
// These exist so the constants the schema enforces come from the code that enforces them. A JSON
// migration file could not reference money.Max, and the two would drift the first time one changed.

// ulid is the Crockford base32 alphabet ULID, 26 characters, as minted by internal/domain/ids.
const ulid = `[0-9A-HJKMNP-TV-Z]{26}`

// idPattern builds the anchored pattern for a kind's identifier.
func idPattern(kind string) string { return `^` + kind + `_` + ulid + `$` }

// networkIDPattern builds the pattern for a network-scoped kind: agt_test_… / agt_live_…
func networkIDPattern(kind string) string { return `^` + kind + `_(test|live)_` + ulid + `$` }

// base58 is a Solana address. Base58 excludes 0, O, I and l.
const base58 = `^[1-9A-HJ-NP-Za-km-z]{32,44}$`

// amount is a money field: int64 base units, positive, and bounded.
//
// bsonType "long" is THE guarantee for invariant I6 — it holds regardless of driver version, shell
// session, or which of the two runtimes wrote the document. The maximum matters as much as the
// minimum: it keeps every sum the admission filter can construct inside the integer range, so
// MongoDB's $sum never promotes to a double and the comparison is never made in floating point.
func amount(minimum int64) bson.D {
	return bson.D{
		{Key: "bsonType", Value: "long"},
		{Key: "minimum", Value: minimum},
		{Key: "maximum", Value: int64(money.Max)},
	}
}

// enum is a string constrained to a set. There is no database enum type here, so adding a value is
// a collMod in a migration rather than an ALTER TYPE.
func enum(values ...string) bson.D {
	vs := make(bson.A, len(values))
	for i, v := range values {
		vs[i] = v
	}
	return bson.D{{Key: "bsonType", Value: "string"}, {Key: "enum", Value: vs}}
}

func str(minLen, maxLen int) bson.D {
	return bson.D{
		{Key: "bsonType", Value: "string"},
		{Key: "minLength", Value: minLen},
		{Key: "maxLength", Value: maxLen},
	}
}

func pattern(p string) bson.D {
	return bson.D{{Key: "bsonType", Value: "string"}, {Key: "pattern", Value: p}}
}

func date() bson.D         { return bson.D{{Key: "bsonType", Value: "date"}} }
func nullableDate() bson.D { return bson.D{{Key: "bsonType", Value: bson.A{"date", "null"}}} }
func boolean() bson.D      { return bson.D{{Key: "bsonType", Value: "bool"}} }
func nullableLong() bson.D { return bson.D{{Key: "bsonType", Value: bson.A{"long", "null"}}} }
func object() bson.D       { return bson.D{{Key: "bsonType", Value: bson.A{"object", "null"}}} }
func nullableStr() bson.D  { return bson.D{{Key: "bsonType", Value: bson.A{"string", "null"}}} }

// schema assembles a $jsonSchema fragment.
//
// additionalProperties is ALWAYS false. A mistyped field name in a $set becomes a write error
// rather than a silently orphaned key, which is the single most common way a MongoDB schema rots.
func schema(required []string, props bson.D) bson.D {
	req := make(bson.A, len(required))
	for i, r := range required {
		req[i] = r
	}
	return bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: req},
		{Key: "additionalProperties", Value: false},
		{Key: "properties", Value: props},
	}
}

// validator combines the type schema with the cross-field checks $jsonSchema cannot express.
//
// JSON Schema has no way to reference a sibling property's value, so every constraint of the form
// "this field relative to that one" lives in $expr. Both are legal inside a validator.
func validator(jsonSchema bson.D, expr ...bson.D) bson.D {
	and := bson.A{bson.D{{Key: "$jsonSchema", Value: jsonSchema}}}
	for _, e := range expr {
		and = append(and, bson.D{{Key: "$expr", Value: e}})
	}
	return bson.D{{Key: "$and", Value: and}}
}

// networkPrefixOf extracts the "test" or "live" segment from an identifier field.
//
//	agt_test_01J8… → "test"
func networkPrefixOf(field string) bson.D {
	return bson.D{{Key: "$arrayElemAt", Value: bson.A{
		bson.D{{Key: "$split", Value: bson.A{field, "_"}}}, 1,
	}}}
}

// expectedPrefix maps a `network` field to the prefix its identifier must carry.
func expectedPrefix(field string) bson.D {
	return bson.D{{Key: "$cond", Value: bson.A{
		bson.D{{Key: "$eq", Value: bson.A{field, "sandbox"}}}, "test", "live",
	}}}
}

// networkMatchesID is invariant I8, as a validator clause.
//
// MongoDB refuses any update that modifies _id, so a record's network is immutable at the storage
// engine — stronger than the trigger the specification asked for. This clause stops the redundant
// `network` field from ever disagreeing with it, so an update setting network:"mainnet" on an
// agt_test_… document fails with DocumentFailedValidation. There is no aggregation-pipeline update
// or $rename that gets around it.
func networkMatchesID() bson.D {
	return bson.D{{Key: "$eq", Value: bson.A{
		networkPrefixOf("$_id"), expectedPrefix("$network"),
	}}}
}

// referenceIsSameNetwork ties a REFERENCED identifier's network to this document's own.
//
// This is the cross-collection check PostgreSQL could only have had as a composite foreign key: a
// mainnet payment cannot reference a sandbox agent, and no $lookup is needed to notice.
func referenceIsSameNetwork(field string) bson.D {
	return bson.D{{Key: "$eq", Value: bson.A{
		networkPrefixOf(field), expectedPrefix("$network"),
	}}}
}

func and(clauses ...bson.D) bson.D {
	a := make(bson.A, len(clauses))
	for i, c := range clauses {
		a[i] = c
	}
	return bson.D{{Key: "$and", Value: a}}
}
