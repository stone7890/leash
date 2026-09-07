package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/stone7890/leash/internal/migrate"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// verify-migrations proves reversibility, and proves it harder than by asserting "down leaves
// nothing".
//
//  1. apply every Up, in order
//  2. SNAPSHOT: every collection WITH ITS VALIDATOR OPTIONS, and every index
//  3. apply every Down, in reverse
//  4. assert nothing of ours survives
//  5. apply every Up again
//  6. assert the snapshot is BYTE-IDENTICAL to the one from step 2
//
// Step 4 catches the classic bug: a Down that forgets something, which breaks the NEXT deploy
// under pressure rather than this one.
//
// Step 6 is the addition, and it is specific to MongoDB. It catches a Down that drops something
// the Up creates as a side effect, so the second Up produces a slightly different schema. That is
// a live hazard here because createCollection-with-a-validator and collMod are different
// operations that can leave different-looking option documents behind — and a schema that differs
// between a fresh database and a migrated one is a schema whose constraints you cannot reason
// about.
func cmdVerifyMigrations(ctx context.Context, _ []string) error {
	st, err := open(ctx)
	if err != nil {
		return err
	}
	defer st.Close(context.Background())
	db := st.Verifier()

	c, cancel := withTimeout(ctx, 10*time.Minute)
	defer cancel()

	fmt.Println("1 · applying every migration")
	if err := migrate.Apply(c, db); err != nil {
		return err
	}

	fmt.Println("2 · snapshotting collections, validators and indexes")
	before, err := snapshotSchema(c, db)
	if err != nil {
		return err
	}
	fmt.Printf("    %d collections\n", countCollections(before))

	fmt.Println("3 · rolling every migration back")
	if err := migrate.Rollback(c, db, 0); err != nil {
		return err
	}

	fmt.Println("4 · asserting nothing of ours survives")
	after, err := snapshotSchema(c, db)
	if err != nil {
		return err
	}
	if n := countCollections(after); n != 0 {
		var left []string
		var m map[string]any
		_ = json.Unmarshal([]byte(after), &m)
		for k := range m {
			left = append(left, k)
		}
		sort.Strings(left)
		return fmt.Errorf(
			"rolling back left %d collection(s) behind: %v\n"+
				"A Down that forgets something breaks the NEXT deploy, not this one", n, left)
	}
	// The ledger must be empty of version rows, but is allowed to exist — migration 1 does not
	// drop it, because Rollback deletes from it as it goes and dropping it would take the lock.
	n, err := db.Collection(migrate.LedgerCollection).CountDocuments(c,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$type", Value: "int"}}}})
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("the migration ledger still records %d applied migration(s)", n)
	}

	fmt.Println("5 · applying every migration again")
	if err := migrate.Apply(c, db); err != nil {
		return err
	}

	fmt.Println("6 · asserting the schema is byte-identical")
	again, err := snapshotSchema(c, db)
	if err != nil {
		return err
	}
	if again != before {
		return fmt.Errorf(
			"the schema differs after down-then-up.\n"+
				"This means a Down drops something its Up creates as a side effect, so a "+
				"migrated database and a fresh one are not the same database.\n\n"+
				"--- first  ---\n%s\n\n--- second ---\n%s", before, again)
	}

	fmt.Printf("\nup · down · up is reversible and reproducible across %d migrations\n",
		len(migrate.All()))
	return nil
}

func countCollections(snapshot string) int {
	var m map[string]any
	if err := json.Unmarshal([]byte(snapshot), &m); err != nil {
		return -1
	}
	n := 0
	for name := range m {
		if name != migrate.LedgerCollection {
			n++
		}
	}
	return n
}

// snapshotSchema renders the whole schema as canonical, sorted JSON.
//
// It includes the VALIDATOR OPTIONS, not just the collection names — the validators are where the
// invariants live, and a snapshot that ignored them would pass while the schema silently lost the
// clause that makes over-admission impossible.
func snapshotSchema(ctx context.Context, db *mongo.Database) (string, error) {
	cur, err := db.ListCollections(ctx, bson.D{})
	if err != nil {
		return "", fmt.Errorf("listing collections: %w", err)
	}
	defer cur.Close(ctx)

	out := map[string]any{}
	for cur.Next(ctx) {
		var spec struct {
			Name    string   `bson:"name"`
			Type    string   `bson:"type"`
			Options bson.Raw `bson:"options"`
		}
		if err := cur.Decode(&spec); err != nil {
			return "", err
		}
		if spec.Name == migrate.LedgerCollection {
			continue // the ledger's contents change every run by design
		}

		entry := map[string]any{"type": spec.Type}
		if len(spec.Options) > 0 {
			var opts map[string]any
			if err := bson.Unmarshal(spec.Options, &opts); err != nil {
				return "", err
			}
			entry["options"] = opts
		}

		idxCur, err := db.Collection(spec.Name).Indexes().List(ctx)
		if err != nil {
			return "", fmt.Errorf("listing indexes on %s: %w", spec.Name, err)
		}
		var indexes []map[string]any
		if err := idxCur.All(ctx, &indexes); err != nil {
			return "", err
		}
		for _, ix := range indexes {
			// The server reports its own version on every index; it is not part of our schema.
			delete(ix, "v")
		}
		sort.Slice(indexes, func(i, j int) bool {
			return fmt.Sprint(indexes[i]["name"]) < fmt.Sprint(indexes[j]["name"])
		})
		entry["indexes"] = indexes
		out[spec.Name] = entry
	}
	if err := cur.Err(); err != nil {
		return "", err
	}

	// json.Marshal sorts map keys, so the rendering is canonical and two snapshots are
	// byte-comparable.
	b, err := json.MarshalIndent(out, "", "  ")
	return string(b), err
}
