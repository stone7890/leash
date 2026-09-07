// Package migrate applies numbered, reversible schema changes.
//
// Migrations run at start-up, BEFORE the port is bound. A service that starts serving and then
// discovers its schema is wrong reports itself healthy while being useless, and a load balancer
// believes it.
//
// They are Go rather than JSON command files for three reasons. The validators here contain $expr
// with $cond, $split and $arrayElemAt, which as raw JSON would be unreadable and unreviewable —
// defeating the point of pushing correctness into the database. A JSON file cannot reference
// money.Max or network.Prefixes(), so the schema's constants would drift from the code's. And a
// JSON runner offers no way to prove reversibility, which CI is required to do.
//
// The file name and the Name field must agree, and a test asserts it: a migration whose name no
// longer describes it is worse than one with no name at all.
package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// LedgerCollection records what has been applied, and holds the boot lock.
const LedgerCollection = "schema_migrations"

// lockID is the _id of the lock document. Its uniqueness is what decides the race between two
// instances booting at once, which on a rolling deploy is the normal case rather than an edge one.
const lockID = "lock"

// staleLockAfter is when a lock may be stolen. A process that died holding one must not block
// every future deploy, but the window is long enough that a slow migration is not interrupted.
const staleLockAfter = 5 * time.Minute

// Migration is one reversible change.
type Migration struct {
	Version int
	Name    string
	Up      func(ctx context.Context, db *mongo.Database) error
	Down    func(ctx context.Context, db *mongo.Database) error
}

var registry []Migration

func register(m Migration) {
	for _, e := range registry {
		if e.Version == m.Version {
			panic(fmt.Sprintf("migrate: version %d is registered twice", m.Version))
		}
	}
	registry = append(registry, m)
}

// All returns every registered migration, in order.
func All() []Migration {
	out := append([]Migration(nil), registry...)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out
}

type record struct {
	Version   int       `bson:"_id"`
	Name      string    `bson:"name"`
	AppliedAt time.Time `bson:"applied_at"`
	Checksum  string    `bson:"checksum"`
}

// lockWait is how long a process waits for another one's migrations before giving up.
//
// Contention here is TRANSIENT by definition: whoever holds the lock is applying migrations and
// will release them in seconds. Every service applies migrations at boot, so on a cold start they
// all race — and a loser that exits turns a normal race into a failed deploy on any platform that
// does not restart it for us.
const lockWait = 90 * time.Second

// Apply brings the database up to date, under a lock.
func Apply(ctx context.Context, db *mongo.Database) error {
	if err := acquireLockWaiting(ctx, db); err != nil {
		return err
	}
	defer releaseLock(ctx, db)

	applied, err := appliedRecords(ctx, db)
	if err != nil {
		return err
	}

	for _, m := range All() {
		sum := checksum(m)
		if rec, ok := applied[m.Version]; ok {
			// A migration edited after it was applied is a divergence between what this database
			// contains and what this build thinks it contains. Refusing to start is the only safe
			// answer: the alternative is development and production quietly disagreeing.
			if rec.Checksum != sum {
				return fmt.Errorf(
					"MIGRATION_FAILED: migration %04d_%s was edited after it was applied "+
						"(recorded %s, now %s). Do not adjust the checksum — work out what diverged",
					m.Version, m.Name, short(rec.Checksum), short(sum))
			}
			if rec.Name != m.Name {
				return fmt.Errorf(
					"MIGRATION_FAILED: migration %04d was applied as %q and is now named %q",
					m.Version, rec.Name, m.Name)
			}
			continue
		}

		start := time.Now()
		if err := m.Up(ctx, db); err != nil {
			return fmt.Errorf("MIGRATION_FAILED: %04d_%s: %w", m.Version, m.Name, err)
		}
		_, err := db.Collection(LedgerCollection).InsertOne(ctx, record{
			Version: m.Version, Name: m.Name, AppliedAt: time.Now().UTC(), Checksum: sum,
		})
		if err != nil {
			return fmt.Errorf("MIGRATION_FAILED: recording %04d_%s: %w", m.Version, m.Name, err)
		}
		slog.Info("migration applied",
			"version", m.Version, "name", m.Name, "took", time.Since(start))
	}
	return nil
}

// acquireLockWaiting keeps trying while somebody else is mid-migration.
//
// It reports what it is waiting for, once, so a slow start is legible rather than silent — and it
// gives up eventually, because a lock that never frees is a different problem and should not look
// like a slow one.
func acquireLockWaiting(ctx context.Context, db *mongo.Database) error {
	deadline := time.Now().Add(lockWait)
	announced := false
	for {
		err := acquireLock(ctx, db)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errLockHeld) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"another process has held the migration lock for %s: %w", lockWait, err)
		}
		if !announced {
			slog.Info("another process is applying migrations — waiting")
			announced = true
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Rollback reverses migrations from the highest applied down to (but not including) `to`.
// `to = 0` reverses everything. Used by verify-migrations, and by an operator who knows why.
func Rollback(ctx context.Context, db *mongo.Database, to int) error {
	if err := acquireLockWaiting(ctx, db); err != nil {
		return err
	}
	defer releaseLock(ctx, db)

	applied, err := appliedRecords(ctx, db)
	if err != nil {
		return err
	}
	all := All()
	for i := len(all) - 1; i >= 0; i-- {
		m := all[i]
		if m.Version <= to {
			break
		}
		if _, ok := applied[m.Version]; !ok {
			continue
		}
		if err := m.Down(ctx, db); err != nil {
			return fmt.Errorf("rolling back %04d_%s: %w", m.Version, m.Name, err)
		}
		if _, err := db.Collection(LedgerCollection).
			DeleteOne(ctx, bson.D{{Key: "_id", Value: m.Version}}); err != nil {
			return fmt.Errorf("un-recording %04d_%s: %w", m.Version, m.Name, err)
		}
		slog.Info("migration rolled back", "version", m.Version, "name", m.Name)
	}
	return nil
}

func appliedRecords(ctx context.Context, db *mongo.Database) (map[int]record, error) {
	cur, err := db.Collection(LedgerCollection).Find(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$type", Value: "int"}}}})
	if err != nil {
		return nil, fmt.Errorf("reading the migration ledger: %w", err)
	}
	defer cur.Close(ctx)

	out := map[int]record{}
	for cur.Next(ctx) {
		var r record
		if err := cur.Decode(&r); err != nil {
			return nil, err
		}
		out[r.Version] = r
	}
	return out, cur.Err()
}

type lockDoc struct {
	ID         string    `bson:"_id"`
	Holder     string    `bson:"holder"`
	AcquiredAt time.Time `bson:"acquired_at"`
}

// acquireLock inserts the lock document. Its unique _id decides the race in the database, which is
// the only place two processes can agree without talking to each other.
func acquireLock(ctx context.Context, db *mongo.Database) error {
	coll := db.Collection(LedgerCollection)
	holder := fmt.Sprintf("%d@%s", time.Now().UnixNano(), hostname())

	_, err := coll.InsertOne(ctx, lockDoc{ID: lockID, Holder: holder, AcquiredAt: time.Now().UTC()})
	if err == nil {
		return nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("acquiring the migration lock: %w", err)
	}

	// Somebody holds it. If it is stale, steal it — loudly. A process that died mid-migration
	// must not block every deploy that follows.
	var existing lockDoc
	if err := coll.FindOne(ctx, bson.D{{Key: "_id", Value: lockID}}).Decode(&existing); err != nil {
		return fmt.Errorf("reading the migration lock: %w", err)
	}
	if time.Since(existing.AcquiredAt) < staleLockAfter {
		// Transient, and the caller waits rather than failing. Wrapped so it is distinguishable
		// from a real error — waiting on a broken database would be worse than stopping.
		return fmt.Errorf("%w (holder %s, since %s)",
			errLockHeld, existing.Holder, existing.AcquiredAt.Format(time.RFC3339))
	}
	slog.Warn("stealing a stale migration lock",
		"holder", existing.Holder, "held_for", time.Since(existing.AcquiredAt))
	res, err := coll.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: lockID}, {Key: "holder", Value: existing.Holder}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "holder", Value: holder},
			{Key: "acquired_at", Value: time.Now().UTC()},
		}}})
	if err != nil {
		return fmt.Errorf("stealing the migration lock: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("%w (another process took it first)", errLockHeld)
	}
	return nil
}

// errLockHeld means somebody else is mid-migration. It is transient, and the caller waits.
var errLockHeld = errors.New("the migration lock is held")

func releaseLock(ctx context.Context, db *mongo.Database) {
	// Best effort with a fresh context: the caller's may already be cancelled, and leaving the
	// lock behind would make the next boot wait five minutes for nothing.
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := db.Collection(LedgerCollection).
		DeleteOne(c, bson.D{{Key: "_id", Value: lockID}}); err != nil {
		slog.Warn("could not release the migration lock", "err", err)
	}
}

// checksum hashes what a migration declares, so an edit after the fact is detectable.
//
// It covers the version and the name. It cannot cover the function bodies — Go has no stable
// representation of those at runtime — so a migration that changes its commands without changing
// its name is NOT caught here. That gap is why `make verify-migrations` re-applies from empty and
// compares the resulting schema: the two mechanisms together cover what neither does alone.
func checksum(m Migration) string {
	b, _ := json.Marshal([]any{m.Version, m.Name})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func hostname() string {
	h, err := osHostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// helpers ────────────────────────────────────────────────────────────────────

func createCollection(ctx context.Context, db *mongo.Database, name string, validator bson.D) error {
	opts := options.CreateCollection().
		SetValidator(validator).
		// strict validates the whole post-image on updates too, so $set and $inc are covered —
		// not just inserts. error refuses the write rather than warning about it.
		SetValidationLevel("strict").
		SetValidationAction("error")
	if err := db.CreateCollection(ctx, name, opts); err != nil {
		return fmt.Errorf("creating %s: %w", name, err)
	}
	return nil
}

func createIndexes(ctx context.Context, db *mongo.Database, name string, models []mongo.IndexModel) error {
	if len(models) == 0 {
		return nil
	}
	if _, err := db.Collection(name).Indexes().CreateMany(ctx, models); err != nil {
		return fmt.Errorf("indexing %s: %w", name, err)
	}
	return nil
}

func dropCollection(ctx context.Context, db *mongo.Database, name string) error {
	if err := db.Collection(name).Drop(ctx); err != nil {
		return fmt.Errorf("dropping %s: %w", name, err)
	}
	return nil
}
