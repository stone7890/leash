package migrate

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

// The migration ledger itself, plus the boot lock.
//
// It comes first because everything after it is recorded here. The _id is either an int — an
// applied version — or the string "lock", and that _id's uniqueness is what decides the race
// between two instances booting at once, which on a rolling deploy is the normal case rather than
// an edge one.
//
// The name of this migration is the rule the whole schema is built to keep: every amount below is
// a `long`, never a double, because a double is how an audit trail stops adding up.
func init() {
	register(Migration{
		Version: 1,
		Name:    "money_is_an_integer_number_of_micro_usdc",
		Up: func(ctx context.Context, db *mongo.Database) error {
			// No validator on the ledger. It holds two different shapes, and constraining it would
			// buy nothing while making a stale lock harder to steal when a process dies holding it.
			err := db.CreateCollection(ctx, LedgerCollection)
			if err != nil && !isNamespaceExists(err) {
				return err
			}
			return nil
		},
		Down: func(ctx context.Context, db *mongo.Database) error {
			// The ledger is deliberately NOT dropped. Rollback deletes this migration's own record
			// from it immediately after this returns, and dropping the collection here would take
			// the lock with it. verify-migrations asserts the ledger is empty of version rows,
			// not that it is absent.
			return nil
		},
	})
}

// isNamespaceExists reports whether CreateCollection lost a race with a concurrent boot, or with
// Apply's own lock insert having created the collection implicitly.
func isNamespaceExists(err error) bool {
	var ce mongo.CommandError
	if errors.As(err, &ce) {
		return ce.Code == 48 || ce.Name == "NamespaceExists"
	}
	return false
}
