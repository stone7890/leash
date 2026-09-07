// Package store is every MongoDB command in the system, on the Go side.
//
// No other Go package imports the driver, and `leashctl verify-architecture` enforces it over the
// real package graph. The check that matters is not the import rule but the type rule: NO DRIVER
// TYPE MAY APPEAR IN THIS PACKAGE'S EXPORTED API. A grep passes happily while a
// `Coll(name) *mongo.Collection` helper exists, at which point the boundary is decorative. So this
// package takes domain values and scalars, and returns domain structs and errors. A caller cannot
// construct a query, because a query is not a thing it accepts.
//
// The TypeScript runtime has its own access layer, and that duplication is deliberate and
// registered (docs/16-deck-conformance.md §D-6). It is survivable because the ENFORCEMENT POINT IS
// THE SERVER: the validators, the partial unique indexes and the role privileges constrain both
// runtimes identically, and neither can weaken them.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

// Store owns the client and the handles onto every collection.
type Store struct {
	client *mongo.Client
	db     *mongo.Database
}

// Connect opens the client and confirms there is a writable primary.
//
// The write concern is majority, and that is not tuneable. A money write acknowledged by one node
// and lost in a failover is a signature we handed out and cannot account for.
//
// The connection string must name a replica set. There is no standalone mode: transactions and
// change streams both need one, and both are load-bearing.
func Connect(ctx context.Context, uri, dbName string) (*Store, error) {
	if !strings.Contains(uri, "replicaSet=") && !strings.Contains(uri, "mongodb+srv://") {
		return nil, fmt.Errorf(
			"the connection string does not name a replica set. Leash has no standalone mode: " +
				"multi-document transactions and change streams both require one, and the kill " +
				"switch depends on a change stream. Add ?replicaSet=... to MONGO_URI")
	}

	opts := options.Client().
		ApplyURI(uri).
		SetWriteConcern(writeconcern.Majority()).
		SetReadConcern(readconcern.Majority()).
		SetReadPreference(readpref.Primary()).
		SetRetryWrites(true).
		SetAppName("leash").
		SetServerSelectionTimeout(10 * time.Second).
		// Every client is built with the money codec, so there is no path that encodes an amount
		// as anything but int64. See money_codec.go.
		SetRegistry(registry())

	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("connecting to MongoDB: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("no writable MongoDB primary: %w", err)
	}

	return &Store{client: client, db: client.Database(dbName)}, nil
}

// Close disconnects. Called on the way out, after the background loops have stopped.
func (s *Store) Close(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Disconnect(ctx)
}

// Ping is the readiness probe's question: is there a primary we can write to?
func (s *Store) Ping(ctx context.Context) error {
	return s.client.Ping(ctx, readpref.Primary())
}

// database is unexported on purpose.
//
// Handing a *mongo.Database to a caller is exactly the leak that makes the store boundary
// decorative, so the only things that reach it are inside this package — plus migrate and
// leashctl, through the narrow accessors below, which verify-architecture allows by name.
func (s *Store) database() *mongo.Database { return s.db }

// ── Errors ───────────────────────────────────────────────────────────────────
//
// Classification is by SQLSTATE-equivalent: the server's error CODE, never its message text. A
// message is prose that changes between releases; a code is an interface.

var (
	// ErrDuplicate is a unique index refusing a second row. It is not a failure — it is usually
	// idempotency working, and the caller decides which.
	ErrDuplicate = errors.New("a unique index refused this write")
	// ErrValidation is a $jsonSchema or $expr validator refusing a document. It always means a
	// bug: the application tried to write something the schema says cannot exist.
	ErrValidation = errors.New("the document failed validation")
	// ErrNotFound is an empty result where one was required.
	ErrNotFound = errors.New("not found")
	// ErrUnauthorized is the append-only guarantee working: the role does not grant this action.
	ErrUnauthorized = errors.New("the database role does not permit this action")
)

// classify maps a driver error onto one of the four above.
func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ErrNotFound
	}
	if mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("%w: %v", ErrDuplicate, err)
	}

	var ce mongo.CommandError
	if errors.As(err, &ce) {
		switch ce.Code {
		case 121: // DocumentValidationFailure
			return fmt.Errorf("%w: %v", ErrValidation, err)
		case 13: // Unauthorized
			return fmt.Errorf("%w: %v", ErrUnauthorized, err)
		}
	}
	var we mongo.WriteException
	if errors.As(err, &we) {
		for _, e := range we.WriteErrors {
			switch e.Code {
			case 121:
				return fmt.Errorf("%w: %v", ErrValidation, err)
			case 13:
				return fmt.Errorf("%w: %v", ErrUnauthorized, err)
			}
		}
	}
	return err
}

// IsDuplicate, IsValidation and IsUnauthorized let callers branch without importing the driver.
func IsDuplicate(err error) bool    { return errors.Is(err, ErrDuplicate) }
func IsValidation(err error) bool   { return errors.Is(err, ErrValidation) }
func IsUnauthorized(err error) bool { return errors.Is(err, ErrUnauthorized) }
func IsNotFound(err error) bool     { return errors.Is(err, ErrNotFound) }

// ── Narrow accessors for the tools that legitimately need the handle ──────────
//
// migrate applies schema changes, and leashctl proves them. Both need the database itself, and
// neither is on a request path. verify-architecture allows these two by name; nothing else may
// call them.

// Migrator hands the raw database to the migration runner.
func (s *Store) Migrator() *mongo.Database { return s.db }

// Verifier hands the raw database to leashctl's proof commands.
func (s *Store) Verifier() *mongo.Database { return s.db }
