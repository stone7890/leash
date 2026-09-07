// Package mongotest gives a test a throwaway database on a real MongoDB replica set.
//
// A real server, not a fake. Everything worth testing about this storage layer — the validators,
// the partial unique indexes, the compare-and-swap's atomicity — is behaviour of MongoDB itself,
// and a fake would be a second implementation of the thing under test.
package mongotest

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stone7890/leash/internal/migrate"
	"github.com/stone7890/leash/internal/store"
)

// URI is where the tests look. `make dev` starts exactly this.
func URI() string {
	if v := os.Getenv("MONGO_URI"); v != "" {
		return v
	}
	return "mongodb://localhost:27017/leash?replicaSet=rs0&directConnection=true"
}

// New opens a store on a database unique to this test, applies the migrations, and drops it
// afterwards.
//
// It SKIPS rather than fails when there is no server, with a printed reason. A developer running
// `go test ./...` without the stack up should be told what is missing, not handed a red suite they
// will learn to ignore.
func New(t *testing.T) *store.Store {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: needs a MongoDB replica set (-short)")
	}

	dbName := fmt.Sprintf("leashtest_%d_%d", time.Now().UnixNano(), os.Getpid())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.Connect(ctx, URI(), dbName)
	if err != nil {
		t.Skipf("skipping: no MongoDB replica set at %s (%v).\n"+
			"Start one with: make dev", URI(), err)
	}

	// The election-timing trap, from docs/14-deployment.md: rs.initiate() returns BEFORE the node
	// is writable, so the first w:majority write fails with NotWritablePrimary. Requiring TWO
	// consecutive successful pings is what makes this harness reliable rather than flaky.
	//
	// Do not remove the doubling for looking redundant. It is not.
	ok := 0
	for i := 0; i < 40 && ok < 2; i++ {
		if err := st.Ping(ctx); err == nil {
			ok++
		} else {
			ok = 0
			time.Sleep(250 * time.Millisecond)
		}
	}
	if ok < 2 {
		t.Skipf("skipping: no writable primary at %s", URI())
	}

	if err := migrate.Apply(ctx, st.Migrator()); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}

	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = st.Migrator().Drop(c)
		_ = st.Close(c)
	})
	return st
}
