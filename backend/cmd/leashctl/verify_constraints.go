package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stone7890/leash/internal/domain/ids"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/migrate"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// verify-constraints performs, against a real server, every write the schema must refuse.
//
// A unique index nobody has watched reject something is a comment. These are the constraints the
// documentation claims, executed, with the refusal asserted by ERROR CODE rather than by message
// text — a message is prose that changes between releases, and a code is an interface.
//
// --selftest runs first in CI: it plants each violation against a deliberately weakened schema and
// asserts the checker NOTICES. Without it, a checker that silently passes everything is
// indistinguishable from a schema that refuses everything.

type refusal struct {
	name string
	// engineEnforced marks a refusal the storage engine performs regardless of our schema — _id
	// immutability, for instance. Those are exempt from the self-test below, because there is no
	// way to weaken the schema enough to let them through, and demanding it would be demanding
	// the impossible rather than checking anything.
	engineEnforced bool
	// want is the server error we require. Asserting the exact code is the difference between
	// "something went wrong" and "the constraint we documented is the one that fired".
	want int
	do   func(ctx context.Context, db *mongo.Database, f *fixture) error
}

const (
	codeDuplicateKey = 11000
	codeValidation   = 121
	codeImmutableID  = 66 // ImmutableField
)

// fixture is a consistent set of parent documents the refusals can reference, so that a write is
// refused for the reason under test rather than for a missing reference.
type fixture struct {
	org       ids.ID
	agent     ids.ID // sandbox
	agentLive ids.ID // mainnet
	allowance ids.ID
	signReq   ids.ID
	payment   ids.ID
	mint      string
	wallet    string
	payTo     string
	now       time.Time
}

func cmdVerifyConstraints(ctx context.Context, args []string) error {
	if has(args, "--selftest") {
		return selftestConstraints(ctx)
	}

	st, err := open(ctx)
	if err != nil {
		return err
	}
	defer st.Close(context.Background())
	db := st.Verifier()

	c, cancel := withTimeout(ctx, 5*time.Minute)
	defer cancel()

	if err := migrate.Apply(c, db); err != nil {
		return err
	}
	f, err := seedFixture(c, db)
	if err != nil {
		return fmt.Errorf("seeding the fixture: %w", err)
	}

	refusals := allRefusals()
	fmt.Printf("%d writes the database must refuse\n\n", len(refusals))

	var failed int
	for _, r := range refusals {
		err := r.do(c, db, f)
		got, ok := serverCode(err)
		switch {
		case err == nil:
			fmt.Printf("  ✗ %-58s ACCEPTED — the constraint is not there\n", r.name)
			failed++
		case !ok:
			fmt.Printf("  ✗ %-58s refused, but not by the server (%v)\n", r.name, err)
			failed++
		case got != r.want:
			fmt.Printf("  ✗ %-58s refused with %d, expected %d\n", r.name, got, r.want)
			failed++
		default:
			fmt.Printf("  ✓ %-58s refused (%d)\n", r.name, got)
		}
	}

	fmt.Println()
	if failed > 0 {
		return fmt.Errorf("%d of %d constraints did not refuse what they claim to refuse",
			failed, len(refusals))
	}
	fmt.Printf("all %d constraints refused what they claim to refuse\n", len(refusals))
	return nil
}

// serverCode extracts MongoDB's own error code.
func serverCode(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var we mongo.WriteException
	if errors.As(err, &we) {
		if we.WriteConcernError != nil {
			return we.WriteConcernError.Code, true
		}
		for _, e := range we.WriteErrors {
			return e.Code, true
		}
	}
	var ce mongo.CommandError
	if errors.As(err, &ce) {
		return int(ce.Code), true
	}
	var be mongo.BulkWriteException
	if errors.As(err, &be) {
		for _, e := range be.WriteErrors {
			return e.Code, true
		}
	}
	return 0, false
}

func seedFixture(ctx context.Context, db *mongo.Database) (*fixture, error) {
	// Start from empty so a re-run is deterministic.
	for _, name := range []string{"orgs", "agents", "agent_keys", "allowances", "policies",
		"policy_revisions", "sign_requests", "sign_claims", "payments", "handshake_events",
		"alerts", "suppressions", "audit_jobs"} {
		if _, err := db.Collection(name).DeleteMany(ctx, bson.D{}); err != nil {
			return nil, err
		}
	}

	f := &fixture{
		mint:   "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
		wallet: "7f3KFa7tyBX3BvBCfmDmXDar8CRq5HunjxE4vU4hEgXa",
		payTo:  "Ex4Y68L2wRXsQ33x5oUz49KwjQ69ZCcwY99MsszehEdF",
		now:    time.Now().UTC(),
	}
	f.org, _ = ids.New(ids.KindOrg)
	f.agent, _ = ids.NewOn(ids.KindAgent, network.Sandbox)
	f.agentLive, _ = ids.NewOn(ids.KindAgent, network.Mainnet)
	f.allowance, _ = ids.NewOn(ids.KindAllowance, network.Sandbox)
	f.signReq, _ = ids.NewOn(ids.KindSignReq, network.Sandbox)
	f.payment, _ = ids.NewOn(ids.KindPayment, network.Sandbox)

	if _, err := db.Collection("orgs").InsertOne(ctx, bson.D{
		{Key: "_id", Value: f.org.String()},
		{Key: "owner_wallet", Value: f.wallet},
		{Key: "plan", Value: "free"},
		{Key: "active_network", Value: "sandbox"},
		{Key: "created_at", Value: f.now},
	}); err != nil {
		return nil, err
	}

	for _, a := range []struct {
		id  ids.ID
		net string
		pub string
	}{
		{f.agent, "sandbox", "Gk7vGySf1TALm7PsiwGJ6f22s9SGi99sdfx9QyWtavWE"},
		{f.agentLive, "mainnet", "Hm8wSuKxXv1eDzFZv2W3FtwpYo3ksasDgwHrp9mWhCNK"},
	} {
		if _, err := db.Collection("agents").InsertOne(ctx, bson.D{
			{Key: "_id", Value: a.id.String()},
			{Key: "org_id", Value: f.org.String()},
			{Key: "name", Value: "research-bot-01"},
			{Key: "template_id", Value: "research"},
			{Key: "network", Value: a.net},
			{Key: "pubkey", Value: a.pub},
			{Key: "idempotency_key", Value: "idem-" + a.id.ULID()},
			{Key: "killed", Value: false},
			{Key: "created_at", Value: f.now},
		}); err != nil {
			return nil, fmt.Errorf("seeding agent %s: %w", a.id, err)
		}
	}

	if _, err := db.Collection("allowances").InsertOne(ctx, validAllowance(f)); err != nil {
		return nil, fmt.Errorf("seeding the allowance: %w", err)
	}
	if _, err := db.Collection("sign_requests").InsertOne(ctx, validSignRequest(f)); err != nil {
		return nil, fmt.Errorf("seeding the sign request: %w", err)
	}
	if _, err := db.Collection("payments").InsertOne(ctx, validPayment(f)); err != nil {
		return nil, fmt.Errorf("seeding the payment: %w", err)
	}
	return f, nil
}
