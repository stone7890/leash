package main

import (
	"context"
	"time"

	"github.com/stone7890/leash/internal/domain/ids"
	"github.com/stone7890/leash/internal/domain/network"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// The documents below are VALID. Each refusal takes one and breaks exactly one thing, so a failure
// says which constraint is missing rather than which document is malformed.

func validAllowance(f *fixture) bson.D {
	return bson.D{
		{Key: "_id", Value: f.allowance.String()},
		{Key: "agent_id", Value: f.agent.String()},
		{Key: "network", Value: "sandbox"},
		{Key: "onchain_addr", Value: "3xk9hSScqsuG9StMc2Qw5536QSdMp74Kndjtv2yS8qU2"},
		{Key: "mint", Value: f.mint},
		{Key: "token_program_id", Value: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"},
		{Key: "cap_base", Value: int64(10_000_000)},
		{Key: "drawn_onchain_base", Value: int64(0)},
		{Key: "reserved_base", Value: int64(0)},
		{Key: "last_read_slot", Value: int64(356442108)},
		{Key: "last_read_at", Value: f.now},
		{Key: "expiry_ts", Value: f.now.Add(7 * 24 * time.Hour)},
		{Key: "expiry_tier", Value: "signer"},
		{Key: "state", Value: "active"},
		{Key: "state_at", Value: f.now},
		{Key: "epoch", Value: int32(1)},
		{Key: "open_key", Value: f.agent.String() + "|" + f.mint},
		{Key: "vel", Value: bson.A{}},
		{Key: "created_at", Value: f.now},
	}
}

func validSignRequest(f *fixture) bson.D {
	return bson.D{
		{Key: "_id", Value: f.signReq.String()},
		{Key: "agent_id", Value: f.agent.String()},
		{Key: "network", Value: "sandbox"},
		{Key: "challenge_hash", Value: "b177922b345e103f6cb62af767b824e61e128e3d1df50e08a38011bae3a82e07"},
		{Key: "verdict", Value: "allowed"},
		{Key: "checks", Value: bson.A{}},
		{Key: "created_at", Value: f.now},
	}
}

func validPayment(f *fixture) bson.D {
	return bson.D{
		{Key: "_id", Value: f.payment.String()},
		{Key: "sign_request_id", Value: f.signReq.String()},
		{Key: "agent_id", Value: f.agent.String()},
		{Key: "allowance_id", Value: f.allowance.String()},
		{Key: "org_id", Value: f.org.String()},
		{Key: "network", Value: "sandbox"},
		{Key: "pay_to", Value: f.payTo},
		{Key: "host", Value: "api.exa.ai"},
		{Key: "agent_name", Value: "research-bot-01"},
		{Key: "amount_base", Value: int64(10_000)},
		{Key: "state", Value: "signed"},
		{Key: "state_at", Value: f.now},
		{Key: "created_at", Value: f.now},
	}
}

// with returns a copy of doc with the named keys replaced or added.
func with(doc bson.D, kv ...bson.E) bson.D {
	out := make(bson.D, 0, len(doc)+len(kv))
	replaced := map[string]bool{}
	for _, e := range doc {
		if v, ok := find(kv, e.Key); ok {
			out = append(out, bson.E{Key: e.Key, Value: v})
			replaced[e.Key] = true
			continue
		}
		out = append(out, e)
	}
	for _, e := range kv {
		if !replaced[e.Key] {
			out = append(out, e)
		}
	}
	return out
}

func find(kv []bson.E, key string) (any, bool) {
	for _, e := range kv {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

// without returns a copy of doc with the named key removed.
func without(doc bson.D, key string) bson.D {
	out := make(bson.D, 0, len(doc))
	for _, e := range doc {
		if e.Key != key {
			out = append(out, e)
		}
	}
	return out
}

func insert(coll string, doc bson.D) func(context.Context, *mongo.Database, *fixture) error {
	return func(ctx context.Context, db *mongo.Database, _ *fixture) error {
		_, err := db.Collection(coll).InsertOne(ctx, doc)
		return err
	}
}

func allRefusals() []refusal {
	return []refusal{
		// ── I8 · the networks never meet ─────────────────────────────────────
		{
			name: "a mainnet payment referencing a sandbox agent",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				id, _ := ids.NewOn(ids.KindPayment, network.Mainnet)
				doc := with(validPayment(f),
					bson.E{Key: "_id", Value: id.String()},
					bson.E{Key: "network", Value: "mainnet"})
				_, err := db.Collection("payments").InsertOne(ctx, doc)
				return err
			},
		},
		{
			// The specification asks for a trigger. MongoDB refuses the _id update outright, and
			// the $expr refuses the field update — so this is caught twice, by two mechanisms.
			name: "updating agents.network",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				_, err := db.Collection("agents").UpdateOne(ctx,
					bson.D{{Key: "_id", Value: f.agent.String()}},
					bson.D{{Key: "$set", Value: bson.D{{Key: "network", Value: "mainnet"}}}})
				return err
			},
		},
		{
			name:           "updating agents._id",
			want:           codeImmutableID,
			engineEnforced: true,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				_, err := db.Collection("agents").UpdateOne(ctx,
					bson.D{{Key: "_id", Value: f.agent.String()}},
					bson.D{{Key: "$set", Value: bson.D{
						{Key: "_id", Value: f.agentLive.String()}}}})
				return err
			},
		},
		{
			name: "a sandbox allowance referencing a mainnet agent",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				id, _ := ids.NewOn(ids.KindAllowance, network.Sandbox)
				doc := with(validAllowance(f),
					bson.E{Key: "_id", Value: id.String()},
					bson.E{Key: "agent_id", Value: f.agentLive.String()},
					bson.E{Key: "onchain_addr", Value: "4yzRiSaRV3avuSDDPKMuYSNSBHVe4ha8ESWCAZ7tJA3S"},
					bson.E{Key: "open_key", Value: f.agentLive.String() + "|" + f.mint})
				_, err := db.Collection("allowances").InsertOne(ctx, doc)
				return err
			},
		},

		// ── The budget invariant ─────────────────────────────────────────────
		{
			name: "drawn greater than the cap",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				_, err := db.Collection("allowances").UpdateOne(ctx,
					bson.D{{Key: "_id", Value: f.allowance.String()}},
					bson.D{{Key: "$set", Value: bson.D{
						{Key: "drawn_onchain_base", Value: int64(10_000_001)}}}})
				return err
			},
		},
		{
			// The highest-value line in the schema. It means the signer's admission filter cannot
			// over-admit even if the filter itself is wrong.
			name: "drawn plus reserved greater than the cap",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				_, err := db.Collection("allowances").UpdateOne(ctx,
					bson.D{{Key: "_id", Value: f.allowance.String()}},
					bson.D{{Key: "$set", Value: bson.D{
						{Key: "drawn_onchain_base", Value: int64(9_000_000)},
						{Key: "reserved_base", Value: int64(1_000_001)}}}})
				return err
			},
		},
		{
			name: "an active allowance with no slot behind it",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				id, _ := ids.NewOn(ids.KindAllowance, network.Sandbox)
				doc := without(with(validAllowance(f),
					bson.E{Key: "_id", Value: id.String()},
					bson.E{Key: "onchain_addr", Value: "5zm1tnzBskkL7HGYLgeSCpjMJCvWTkZhnNLbAPcEAYiM"},
				), "last_read_slot")
				_, err := db.Collection("allowances").InsertOne(ctx, doc)
				return err
			},
		},
		{
			name: "a revoked allowance that still holds its open key",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				_, err := db.Collection("allowances").UpdateOne(ctx,
					bson.D{{Key: "_id", Value: f.allowance.String()}},
					bson.D{{Key: "$set", Value: bson.D{{Key: "state", Value: "revoked"}}}})
				return err
			},
		},

		// ── Uniqueness ───────────────────────────────────────────────────────
		{
			name: "a second live allowance for one agent and mint",
			want: codeDuplicateKey,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				id, _ := ids.NewOn(ids.KindAllowance, network.Sandbox)
				doc := with(validAllowance(f),
					bson.E{Key: "_id", Value: id.String()},
					bson.E{Key: "onchain_addr", Value: "6an25UDTVLeKctrD8udia6SXRnQU2ZkR1QESwD27UzfP"})
				_, err := db.Collection("allowances").InsertOne(ctx, doc)
				return err
			},
		},
		{
			name: "a second sign request for one challenge hash",
			want: codeDuplicateKey,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				id, _ := ids.NewOn(ids.KindSignReq, network.Sandbox)
				doc := with(validSignRequest(f), bson.E{Key: "_id", Value: id.String()})
				_, err := db.Collection("sign_requests").InsertOne(ctx, doc)
				return err
			},
		},
		{
			name: "a second payment for one sign request",
			want: codeDuplicateKey,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				id, _ := ids.NewOn(ids.KindPayment, network.Sandbox)
				doc := with(validPayment(f), bson.E{Key: "_id", Value: id.String()})
				_, err := db.Collection("payments").InsertOne(ctx, doc)
				return err
			},
		},
		{
			name: "a second payment with the same signature",
			want: codeDuplicateKey,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				const sig = "5KdWbxNiZx5G42SeJrzQhcUTn9kVhzRoW4HwNXUT6Mxu"
				if _, err := db.Collection("payments").UpdateOne(ctx,
					bson.D{{Key: "_id", Value: f.payment.String()}},
					bson.D{{Key: "$set", Value: bson.D{{Key: "signature", Value: sig}}}}); err != nil {
					return err
				}
				id, _ := ids.NewOn(ids.KindPayment, network.Sandbox)
				sr, _ := ids.NewOn(ids.KindSignReq, network.Sandbox)
				if _, err := db.Collection("sign_requests").InsertOne(ctx, with(validSignRequest(f),
					bson.E{Key: "_id", Value: sr.String()},
					bson.E{Key: "challenge_hash", Value: "c288a33c456f214f7dc73bf878c935f72f239f4e2ef61f19b49122cbf4b93f18"},
				)); err != nil {
					return err
				}
				doc := with(validPayment(f),
					bson.E{Key: "_id", Value: id.String()},
					bson.E{Key: "sign_request_id", Value: sr.String()},
					bson.E{Key: "signature", Value: sig})
				_, err := db.Collection("payments").InsertOne(ctx, doc)
				return err
			},
		},
		{
			// This is what makes every indexer job body safely re-runnable: a dropped tick or a
			// retried iteration needs no reasoning about, because a duplicate phase is refused.
			name: "a second handshake event for one payment and phase",
			want: codeDuplicateKey,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				for i := 0; i < 2; i++ {
					id, _ := ids.New(ids.KindHandshake)
					if _, err := db.Collection("handshake_events").InsertOne(ctx, bson.D{
						{Key: "_id", Value: id.String()},
						{Key: "payment_id", Value: f.payment.String()},
						{Key: "network", Value: "sandbox"},
						{Key: "phase", Value: "challenge_received"},
						{Key: "writer", Value: "signer"},
						{Key: "at", Value: f.now},
					}); err != nil {
						return err
					}
				}
				return nil
			},
		},

		// ── I4 · confirmed is a claim about the chain ────────────────────────
		{
			name: "a confirmed phase with no slot",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				id, _ := ids.New(ids.KindHandshake)
				_, err := db.Collection("handshake_events").InsertOne(ctx, bson.D{
					{Key: "_id", Value: id.String()},
					{Key: "payment_id", Value: f.payment.String()},
					{Key: "network", Value: "sandbox"},
					{Key: "phase", Value: "confirmed"},
					{Key: "writer", Value: "indexer"},
					{Key: "at", Value: f.now},
				})
				return err
			},
		},
		{
			name: "a confirmed phase written by the signer",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				id, _ := ids.New(ids.KindHandshake)
				_, err := db.Collection("handshake_events").InsertOne(ctx, bson.D{
					{Key: "_id", Value: id.String()},
					{Key: "payment_id", Value: f.payment.String()},
					{Key: "network", Value: "sandbox"},
					{Key: "phase", Value: "confirmed"},
					{Key: "writer", Value: "signer"},
					{Key: "read_back_slot", Value: int64(356442231)},
					{Key: "at", Value: f.now},
				})
				return err
			},
		},
		{
			name: "a confirmed payment with no slot",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				_, err := db.Collection("payments").UpdateOne(ctx,
					bson.D{{Key: "_id", Value: f.payment.String()}},
					bson.D{{Key: "$set", Value: bson.D{{Key: "state", Value: "confirmed"}}}})
				return err
			},
		},

		// ── I6 · money is an integer ─────────────────────────────────────────
		{
			name: "an amount of zero",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				id, _ := ids.NewOn(ids.KindPayment, network.Sandbox)
				sr, _ := ids.NewOn(ids.KindSignReq, network.Sandbox)
				doc := with(validPayment(f),
					bson.E{Key: "_id", Value: id.String()},
					bson.E{Key: "sign_request_id", Value: sr.String()},
					bson.E{Key: "amount_base", Value: int64(0)})
				_, err := db.Collection("payments").InsertOne(ctx, doc)
				return err
			},
		},
		{
			name: "an amount stored as a double",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				id, _ := ids.NewOn(ids.KindPayment, network.Sandbox)
				sr, _ := ids.NewOn(ids.KindSignReq, network.Sandbox)
				doc := with(validPayment(f),
					bson.E{Key: "_id", Value: id.String()},
					bson.E{Key: "sign_request_id", Value: sr.String()},
					bson.E{Key: "amount_base", Value: 0.31})
				_, err := db.Collection("payments").InsertOne(ctx, doc)
				return err
			},
		},
		{
			// The I6 trap, made into a proof. bson.M{"amount_base": 5} is a Go `int`, and how the
			// driver encodes that has varied between versions. bsonType "long" refuses it
			// regardless — which is why the guarantee lives in the database and not in the Go type.
			name: "an amount written as an untyped integer literal",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				id, _ := ids.NewOn(ids.KindPayment, network.Sandbox)
				sr, _ := ids.NewOn(ids.KindSignReq, network.Sandbox)
				doc := with(validPayment(f),
					bson.E{Key: "_id", Value: id.String()},
					bson.E{Key: "sign_request_id", Value: sr.String()},
					bson.E{Key: "amount_base", Value: int32(10000)})
				_, err := db.Collection("payments").InsertOne(ctx, doc)
				return err
			},
		},

		// ── Shape ────────────────────────────────────────────────────────────
		{
			// A mistyped field name in a $set becomes a write error rather than a silently
			// orphaned key. This is the single most common way a MongoDB schema rots.
			name: "an unknown field on an agent",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				_, err := db.Collection("agents").UpdateOne(ctx,
					bson.D{{Key: "_id", Value: f.agent.String()}},
					bson.D{{Key: "$set", Value: bson.D{{Key: "kiled", Value: true}}}})
				return err
			},
		},
		{
			name: "a blocked sign request that does not name its rule",
			want: codeValidation,
			do: func(ctx context.Context, db *mongo.Database, f *fixture) error {
				id, _ := ids.NewOn(ids.KindSignReq, network.Sandbox)
				doc := with(validSignRequest(f),
					bson.E{Key: "_id", Value: id.String()},
					bson.E{Key: "challenge_hash", Value: "d399b44d567032508ed84cf989da46f83f34af5f3fa72f2ac5a233dcf5ca4029"},
					bson.E{Key: "verdict", Value: "blocked"})
				_, err := db.Collection("sign_requests").InsertOne(ctx, doc)
				return err
			},
		},
	}
}
