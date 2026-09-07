package main

import (
	"context"
	"fmt"
	"time"

	"github.com/stone7890/leash/internal/store"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// The self-test for verify-constraints, and it runs FIRST in CI.
//
// The question it answers is not "does the schema refuse these writes" — that is what
// verify-constraints itself asks. It is the prior question: **would this checker notice if the
// schema stopped refusing them?**
//
// A refusal that fails for the wrong reason — a malformed document, a missing reference, a typo in
// a field name — passes verify-constraints while proving nothing. So each refusal is replayed
// against a database with the SAME collections and NO validators and NO indexes, and each must be
// ACCEPTED there. A write that is still refused on an unconstrained schema was never testing a
// constraint.
//
// This is not ceremony. The sibling project's equivalent gate once passed everything because a
// regex error made grep exit non-zero, which reads identically to "no match".
func selftestConstraints(ctx context.Context) error {
	st, err := open(ctx)
	if err != nil {
		return err
	}
	defer st.Close(context.Background())

	// A scratch database alongside the real one: same shapes, none of the rules.
	scratch := st.Verifier().Client().Database(mongoDB() + "_selftest")
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = scratch.Drop(c)
	}()

	c, cancel := withTimeout(ctx, 5*time.Minute)
	defer cancel()

	if err := scratch.Drop(c); err != nil {
		return err
	}
	// Created plainly: no validator, no validation level, no indexes. Everything the real schema
	// refuses, this one accepts.
	for _, name := range []string{"orgs", "agents", "agent_keys", "allowances", "policies",
		"policy_revisions", "sign_requests", "sign_claims", "payments", "handshake_events",
		"alerts", "suppressions", "audit_jobs", "templates"} {
		if err := scratch.CreateCollection(c, name); err != nil {
			return fmt.Errorf("creating the unconstrained %s: %w", name, err)
		}
	}

	f, err := seedFixtureUnconstrained(c, scratch)
	if err != nil {
		return fmt.Errorf("seeding the unconstrained fixture: %w", err)
	}

	refusals := allRefusals()
	fmt.Printf("self-test: %d refusals, replayed against a schema with no rules\n", len(refusals))
	fmt.Print("each must be ACCEPTED there, or it was never testing a constraint\n\n")

	var broken int
	for _, r := range refusals {
		if r.engineEnforced {
			fmt.Printf("  – %-58s exempt (the storage engine enforces it)\n", r.name)
			continue
		}
		err := r.do(c, scratch, f)
		if err == nil {
			fmt.Printf("  ✓ %-58s accepted, so the real refusal is attributable\n", r.name)
			continue
		}
		code, _ := serverCode(err)
		fmt.Printf("  ✗ %-58s STILL REFUSED (%d): %v\n", r.name, code, err)
		broken++
	}

	fmt.Println()
	if broken > 0 {
		return fmt.Errorf(
			"%d refusal(s) fail even with no constraints in place.\n"+
				"Those checks would pass verify-constraints while proving nothing — fix the "+
				"document they write before trusting the result", broken)
	}
	fmt.Printf("all %d checks isolate the constraint they claim to test\n",
		len(refusals)-countEngineEnforced(refusals))
	return nil
}

func countEngineEnforced(rs []refusal) int {
	n := 0
	for _, r := range rs {
		if r.engineEnforced {
			n++
		}
	}
	return n
}

// seedFixtureUnconstrained mirrors seedFixture, but tolerates a database with no rules.
func seedFixtureUnconstrained(ctx context.Context, db *mongo.Database) (*fixture, error) {
	f, err := seedFixture(ctx, db)
	if err != nil {
		return nil, err
	}
	// The real seed leaves an active allowance holding its open key. Nothing here depends on the
	// indexes, so there is nothing further to do — but assert the fixture landed, because a silent
	// no-op would make every subsequent "accepted" meaningless.
	n, err := db.Collection("agents").CountDocuments(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	if n != 2 {
		return nil, fmt.Errorf("expected two seeded agents, found %d", n)
	}
	return f, nil
}

var _ = store.IsDuplicate
