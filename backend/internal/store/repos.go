package store

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/stone7890/leash/internal/domain/ids"
	"github.com/stone7890/leash/internal/domain/money"
	"github.com/stone7890/leash/internal/domain/network"
	"github.com/stone7890/leash/internal/domain/policy"
	"github.com/stone7890/leash/internal/domain/state"
	"github.com/stone7890/leash/internal/domain/tier"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ── Organisations ────────────────────────────────────────────────────────────

// UpsertOrg finds or creates the organisation for a wallet. The wallet IS the account: there is no
// password and no email address anywhere in the system.
func (s *Store) UpsertOrg(ctx context.Context, wallet string, now time.Time) (Org, error) {
	id, err := ids.New(ids.KindOrg)
	if err != nil {
		return Org{}, err
	}
	var doc orgDoc
	err = s.db.Collection("orgs").FindOneAndUpdate(ctx,
		bson.D{{Key: "owner_wallet", Value: wallet}},
		bson.D{{Key: "$setOnInsert", Value: bson.D{
			{Key: "_id", Value: id.String()},
			{Key: "owner_wallet", Value: wallet},
			{Key: "plan", Value: "free"},
			{Key: "active_network", Value: string(network.Sandbox)},
			{Key: "created_at", Value: now},
		}}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&doc)
	if err != nil {
		return Org{}, classify(err)
	}
	return Org{
		ID: doc.ID, OwnerWallet: doc.OwnerWallet, Plan: doc.Plan,
		ActiveNetwork: network.Network(doc.ActiveNetwork),
		WorkspaceName: doc.WorkspaceName, CreatedAt: doc.CreatedAt,
	}, nil
}

type orgDoc struct {
	ID            string    `bson:"_id"`
	OwnerWallet   string    `bson:"owner_wallet"`
	Plan          string    `bson:"plan"`
	ActiveNetwork string    `bson:"active_network"`
	WorkspaceName string    `bson:"workspace_name,omitempty"`
	CreatedAt     time.Time `bson:"created_at"`
}

// ── Agents ───────────────────────────────────────────────────────────────────

// NewAgent is what the API supplies to create one.
type NewAgent struct {
	OrgID          string
	Name           string
	Template       string
	Network        network.Network
	RunsAs         string
	Pubkey         string
	IdempotencyKey string
	// KeyHash is the SHA-256 of the API key. The key itself is shown once and never stored.
	KeyHash    string
	WrappedKey []byte
	KMSKeyRef  string
	Policy     Policy
	ExpiryTier tier.Tier
}

// CreateAgent writes the agent, its key and its policy in ONE transaction.
//
// A half-created agent — one with no key, or no policy — is an agent the signer would refuse in a
// way nobody could diagnose. The three documents land together or not at all.
func (s *Store) CreateAgent(ctx context.Context, n NewAgent, now time.Time) (Agent, error) {
	id, err := ids.NewOn(ids.KindAgent, n.Network)
	if err != nil {
		return Agent{}, err
	}

	sess, err := s.client.StartSession()
	if err != nil {
		return Agent{}, classify(err)
	}
	defer sess.EndSession(ctx)

	out, err := sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		if _, err := s.db.Collection("agents").InsertOne(sc, bson.D{
			{Key: "_id", Value: id.String()},
			{Key: "org_id", Value: n.OrgID},
			{Key: "name", Value: n.Name},
			{Key: "template_id", Value: n.Template},
			{Key: "network", Value: n.Network.String()},
			{Key: "runs_as", Value: n.RunsAs},
			{Key: "pubkey", Value: n.Pubkey},
			{Key: "idempotency_key", Value: n.IdempotencyKey},
			{Key: "killed", Value: false},
			{Key: "created_at", Value: now},
		}); err != nil {
			return nil, err
		}
		if _, err := s.db.Collection("agent_keys").InsertOne(sc, bson.D{
			{Key: "_id", Value: id.String()},
			{Key: "kms_key_ref", Value: n.KMSKeyRef},
			{Key: "wrapped_key", Value: bson.Binary{Subtype: 0x00, Data: n.WrappedKey}},
			{Key: "key_hash", Value: n.KeyHash},
			{Key: "created_at", Value: now},
		}); err != nil {
			return nil, err
		}
		if _, err := s.db.Collection("policies").InsertOne(sc, policyDocOf(id.String(), n.Policy, now)); err != nil {
			return nil, err
		}
		rev, _ := ids.New(ids.KindRevision)
		if _, err := s.db.Collection("policy_revisions").InsertOne(sc, bson.D{
			{Key: "_id", Value: rev.String()},
			{Key: "agent_id", Value: id.String()},
			{Key: "change", Value: bson.D{{Key: "created", Value: n.Template}}},
			{Key: "actor", Value: "owner"},
			{Key: "at", Value: now},
		}); err != nil {
			return nil, err
		}
		return id.String(), nil
	})
	if err != nil {
		return Agent{}, classify(err)
	}
	_ = out

	return Agent{
		ID: id.String(), OrgID: n.OrgID, Name: n.Name, Template: n.Template,
		Network: n.Network, RunsAs: n.RunsAs, Pubkey: n.Pubkey, CreatedAt: now,
	}, nil
}

func policyDocOf(agentID string, p Policy, now time.Time) bson.D {
	hosts := bson.A{}
	for _, h := range p.AllowHosts {
		hosts = append(hosts, h)
	}
	d := bson.D{
		{Key: "_id", Value: agentID},
		{Key: "allow_hosts", Value: hosts},
		{Key: "allow_all", Value: p.AllowAll},
		{Key: "per_tx_max_base", Value: int64(p.PerTxMax)},
		{Key: "velocity_max_base", Value: int64(p.VelocityMax)},
		{Key: "velocity_window_s", Value: int32(p.VelocityWindow / time.Second)},
		{Key: "updated_at", Value: now},
	}
	if len(p.AllowPayTo) > 0 {
		pay := bson.A{}
		for _, a := range p.AllowPayTo {
			pay = append(pay, a)
		}
		d = append(d, bson.E{Key: "allow_pay_to", Value: pay})
	}
	return d
}

func (s *Store) GetAgent(ctx context.Context, agentID string) (Agent, error) {
	var d agentDoc
	if err := s.db.Collection("agents").FindOne(ctx,
		bson.D{{Key: "_id", Value: agentID}}).Decode(&d); err != nil {
		return Agent{}, classify(err)
	}
	return d.toAgent(), nil
}

func (s *Store) ListAgents(ctx context.Context, orgID string, net network.Network) ([]Agent, error) {
	cur, err := s.db.Collection("agents").Find(ctx,
		bson.D{{Key: "org_id", Value: orgID}, {Key: "network", Value: net.String()}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, classify(err)
	}
	defer cur.Close(ctx)
	var docs []agentDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, classify(err)
	}
	out := make([]Agent, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.toAgent())
	}
	return out, nil
}

// KillAgent sets the flag the signer refuses on.
//
// It happens the moment the owner confirms, BEFORE the revoke transaction is even built, so the
// soft tier engages even if they then abandon the wallet prompt. The signer's change stream
// evicts the cached snapshot within milliseconds — a 60-second TTL would be far too slow for a
// control whose whole promise is "instantly".
func (s *Store) KillAgent(ctx context.Context, agentID string, now time.Time) error {
	_, err := s.db.Collection("agents").UpdateOne(ctx,
		bson.D{{Key: "_id", Value: agentID}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "killed", Value: true},
			{Key: "killed_at", Value: now},
		}}})
	return classify(err)
}

type agentDoc struct {
	ID        string     `bson:"_id"`
	OrgID     string     `bson:"org_id"`
	Name      string     `bson:"name"`
	Template  string     `bson:"template_id"`
	Network   string     `bson:"network"`
	RunsAs    string     `bson:"runs_as,omitempty"`
	Pubkey    string     `bson:"pubkey"`
	Killed    bool       `bson:"killed"`
	KilledAt  *time.Time `bson:"killed_at,omitempty"`
	CreatedAt time.Time  `bson:"created_at"`
}

func (d agentDoc) toAgent() Agent {
	a := Agent{
		ID: d.ID, OrgID: d.OrgID, Name: d.Name, Template: d.Template,
		Network: network.Network(d.Network), RunsAs: d.RunsAs, Pubkey: d.Pubkey,
		Killed: d.Killed, CreatedAt: d.CreatedAt,
	}
	if d.KilledAt != nil {
		a.KilledAt = *d.KilledAt
	}
	return a
}

// ── Allowances, creation and lookup ──────────────────────────────────────────

type NewAllowance struct {
	AgentID      string
	Network      network.Network
	OnchainAddr  string
	Mint         string
	TokenProgram string
	Cap          money.Base
	ExpiryTS     time.Time
	ExpiryTier   tier.Tier
}

// CreateAllowance writes it as PENDING. It becomes active only through a read-back, and the schema
// refuses an active allowance with no slot — so there is no path around that.
func (s *Store) CreateAllowance(ctx context.Context, n NewAllowance, now time.Time) (Allowance, error) {
	id, err := ids.NewOn(ids.KindAllowance, n.Network)
	if err != nil {
		return Allowance{}, err
	}
	doc := bson.D{
		{Key: "_id", Value: id.String()},
		{Key: "agent_id", Value: n.AgentID},
		{Key: "network", Value: n.Network.String()},
		{Key: "onchain_addr", Value: n.OnchainAddr},
		{Key: "mint", Value: n.Mint},
		{Key: "token_program_id", Value: n.TokenProgram},
		{Key: "cap_base", Value: int64(n.Cap)},
		{Key: "drawn_onchain_base", Value: int64(0)},
		{Key: "reserved_base", Value: int64(0)},
		{Key: "expiry_tier", Value: string(n.ExpiryTier)},
		{Key: "state", Value: string(state.AllowancePending)},
		{Key: "state_at", Value: now},
		{Key: "epoch", Value: int32(1)},
		// Present exactly while the allowance can still spend. The unique partial index over this
		// field is what stops one agent holding two live budgets for one mint.
		{Key: "open_key", Value: n.AgentID + "|" + n.Mint},
		{Key: "vel", Value: bson.A{}},
		{Key: "created_at", Value: now},
	}
	if !n.ExpiryTS.IsZero() {
		doc = append(doc, bson.E{Key: "expiry_ts", Value: n.ExpiryTS})
	}
	if _, err := s.db.Collection("allowances").InsertOne(ctx, doc); err != nil {
		return Allowance{}, classify(err)
	}
	return Allowance{
		ID: id.String(), AgentID: n.AgentID, Network: n.Network, OnchainAddr: n.OnchainAddr,
		Mint: n.Mint, TokenProgram: n.TokenProgram, Cap: n.Cap, ExpiryTS: n.ExpiryTS,
		ExpiryTier: n.ExpiryTier, State: state.AllowancePending, StateAt: now, Epoch: 1,
		CreatedAt: now,
	}, nil
}

func (s *Store) LiveAllowance(ctx context.Context, agentID string) (Allowance, error) {
	var d allowanceDoc
	if err := s.db.Collection("allowances").FindOne(ctx, bson.D{
		{Key: "agent_id", Value: agentID},
		{Key: "state", Value: bson.D{{Key: "$in", Value: bson.A{
			string(state.AllowancePending), string(state.AllowanceActive)}}}},
	}).Decode(&d); err != nil {
		return Allowance{}, classify(err)
	}
	return d.toAllowance(), nil
}

// AllowancesToRefresh is what the 60-second loop reads: everything that can still change.
func (s *Store) AllowancesToRefresh(ctx context.Context, net network.Network, limit int64) ([]Allowance, error) {
	cur, err := s.db.Collection("allowances").Find(ctx, bson.D{
		{Key: "network", Value: net.String()},
		{Key: "state", Value: bson.D{{Key: "$in", Value: bson.A{
			string(state.AllowancePending), string(state.AllowanceActive)}}}},
	}, options.Find().SetSort(bson.D{{Key: "last_read_at", Value: 1}}).SetLimit(limit))
	if err != nil {
		return nil, classify(err)
	}
	defer cur.Close(ctx)
	var docs []allowanceDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, classify(err)
	}
	out := make([]Allowance, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.toAllowance())
	}
	return out, nil
}

// ── The signer's snapshot ────────────────────────────────────────────────────

// SnapshotForKey reads agent, policy and live allowance in ONE round trip.
//
// Three separate finds would be three round trips on a path with a 300ms budget and only three to
// spend. The $lookup does it in one.
func (s *Store) SnapshotForKey(ctx context.Context, keyHash string, now time.Time) (Snapshot, error) {
	cur, err := s.db.Collection("agent_keys").Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "key_hash", Value: keyHash}}}},
		{{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: "agents"}, {Key: "localField", Value: "_id"},
			{Key: "foreignField", Value: "_id"}, {Key: "as", Value: "agent"}}}},
		{{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: "policies"}, {Key: "localField", Value: "_id"},
			{Key: "foreignField", Value: "_id"}, {Key: "as", Value: "policy"}}}},
		{{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: "allowances"},
			{Key: "let", Value: bson.D{{Key: "aid", Value: "$_id"}}},
			{Key: "pipeline", Value: mongo.Pipeline{
				{{Key: "$match", Value: bson.D{{Key: "$expr", Value: bson.D{{Key: "$and", Value: bson.A{
					bson.D{{Key: "$eq", Value: bson.A{"$agent_id", "$$aid"}}},
					bson.D{{Key: "$in", Value: bson.A{"$state", bson.A{"pending", "active"}}}},
				}}}}}}},
			}},
			{Key: "as", Value: "allowance"}}}},
	})
	if err != nil {
		return Snapshot{}, classify(err)
	}
	defer cur.Close(ctx)

	var rows []struct {
		Agent     []agentDoc     `bson:"agent"`
		Policy    []policyDoc    `bson:"policy"`
		Allowance []allowanceDoc `bson:"allowance"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return Snapshot{}, classify(err)
	}
	if len(rows) == 0 || len(rows[0].Agent) == 0 {
		return Snapshot{}, ErrNotFound
	}

	snap := Snapshot{Agent: rows[0].Agent[0].toAgent(), TakenAt: now}
	if len(rows[0].Policy) > 0 {
		snap.Policy = rows[0].Policy[0].toPolicy()
	}
	if len(rows[0].Allowance) > 0 {
		snap.Allowance = rows[0].Allowance[0].toAllowance()
		snap.Velocity = rows[0].Allowance[0].velocity()
	}
	return snap, nil
}

type policyDoc struct {
	AgentID        string     `bson:"_id"`
	AllowHosts     []string   `bson:"allow_hosts"`
	AllowPayTo     []string   `bson:"allow_pay_to,omitempty"`
	AllowAll       bool       `bson:"allow_all"`
	PerTxMax       money.Base `bson:"per_tx_max_base"`
	VelocityMax    money.Base `bson:"velocity_max_base"`
	VelocityWindow int32      `bson:"velocity_window_s"`
	UpdatedAt      time.Time  `bson:"updated_at"`
}

func (d policyDoc) toPolicy() Policy {
	return Policy{
		AgentID: d.AgentID, AllowHosts: d.AllowHosts, AllowPayTo: d.AllowPayTo,
		AllowAll: d.AllowAll, PerTxMax: d.PerTxMax, VelocityMax: d.VelocityMax,
		VelocityWindow: time.Duration(d.VelocityWindow) * time.Second,
		UpdatedAt:      d.UpdatedAt,
	}
}

func (s *Store) GetPolicy(ctx context.Context, agentID string) (Policy, error) {
	var d policyDoc
	if err := s.db.Collection("policies").FindOne(ctx,
		bson.D{{Key: "_id", Value: agentID}}).Decode(&d); err != nil {
		return Policy{}, classify(err)
	}
	return d.toPolicy(), nil
}

// AddAllowedHost adds EXACTLY ONE host, and writes the revision in the same transaction.
//
// The one-host rule is enforced HERE as well as in the interface, because the recovery button is
// not the only caller. The trap the specification names for this flow is a button that quietly
// widens the allow-list beyond the host that was actually blocked.
func (s *Store) AddAllowedHost(ctx context.Context, agentID, host, actor string, now time.Time) error {
	sess, err := s.client.StartSession()
	if err != nil {
		return classify(err)
	}
	defer sess.EndSession(ctx)

	_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		// $addToSet, so pressing the button twice is not an error and does not duplicate.
		if _, err := s.db.Collection("policies").UpdateOne(sc,
			bson.D{{Key: "_id", Value: agentID}},
			bson.D{
				{Key: "$addToSet", Value: bson.D{{Key: "allow_hosts", Value: host}}},
				{Key: "$set", Value: bson.D{{Key: "updated_at", Value: now}}},
			}); err != nil {
			return nil, err
		}
		rev, _ := ids.New(ids.KindRevision)
		_, err := s.db.Collection("policy_revisions").InsertOne(sc, bson.D{
			{Key: "_id", Value: rev.String()},
			{Key: "agent_id", Value: agentID},
			{Key: "change", Value: bson.D{{Key: "allow_host_added", Value: host}}},
			{Key: "actor", Value: actor},
			{Key: "at", Value: now},
		})
		return nil, err
	})
	return classify(err)
}

// ── Claims · invariant I7 ────────────────────────────────────────────────────

// ClaimChallenge is how a request wins the right to sign.
//
// Inserting the claim is the claim: the _id is the challenge hash, so the database decides the
// race and two processes cannot both win. A duplicate means somebody got there first, and the
// answer depends on whether they finished — a settled claim replays its stored response verbatim,
// and an in-flight one is a 409 the SDK must retry WITH THE SAME CHALLENGE.
func (s *Store) ClaimChallenge(ctx context.Context, hash, agentID string,
	net network.Network, blockhash string, now time.Time) (Claim, error) {

	_, err := s.db.Collection("sign_claims").InsertOne(ctx, bson.D{
		{Key: "_id", Value: hash},
		{Key: "agent_id", Value: agentID},
		{Key: "network", Value: net.String()},
		{Key: "state", Value: "in_flight"},
		// Pinned so a retry after a crash rebuilds the IDENTICAL message. Ed25519 is
		// deterministic, so with the blockhash fixed, re-signing produces byte-identical output —
		// one signature, produced twice, rather than two signatures.
		{Key: "blockhash", Value: blockhash},
		{Key: "created_at", Value: now},
	})
	if err == nil {
		return Claim{Won: true, Blockhash: blockhash}, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return Claim{}, classify(err)
	}

	var d claimDoc
	if err := s.db.Collection("sign_claims").FindOne(ctx,
		bson.D{{Key: "_id", Value: hash}}).Decode(&d); err != nil {
		return Claim{}, classify(err)
	}
	if d.State == "done" {
		return Claim{Replay: d.Response, Blockhash: d.Blockhash}, nil
	}
	return Claim{InFlight: true, Blockhash: d.Blockhash}, nil
}

// SettleClaim stores the answer a retry will be given.
func (s *Store) SettleClaim(ctx context.Context, hash string, response map[string]any,
	signRequestID string, now time.Time) error {
	_, err := s.db.Collection("sign_claims").UpdateOne(ctx,
		bson.D{{Key: "_id", Value: hash}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "state", Value: "done"},
			{Key: "response", Value: response},
			{Key: "sign_request_id", Value: signRequestID},
			{Key: "settled_at", Value: now},
		}}})
	return classify(err)
}

// ReleaseStaleClaims frees claims left behind by a crash between claiming and recording.
func (s *Store) ReleaseStaleClaims(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := s.db.Collection("sign_claims").DeleteMany(ctx, bson.D{
		{Key: "state", Value: "in_flight"},
		{Key: "created_at", Value: bson.D{{Key: "$lt", Value: olderThan}}},
	})
	if err != nil {
		return 0, classify(err)
	}
	return res.DeletedCount, nil
}

type claimDoc struct {
	ID        string         `bson:"_id"`
	State     string         `bson:"state"`
	Blockhash string         `bson:"blockhash,omitempty"`
	Response  map[string]any `bson:"response,omitempty"`
}

// ── Verdicts and payments ────────────────────────────────────────────────────

// RecordVerdict writes the append-only sign request and, when allowed, the payment.
//
// One transaction: a payment whose verdict record is missing would be a signature with no
// justification behind it, which is the one thing an audit trail may not contain.
func (s *Store) RecordVerdict(ctx context.Context, sr SignRequest, p *Payment, now time.Time) (
	signRequestID string, paymentID string, err error) {

	srID, err := ids.NewOn(ids.KindSignReq, sr.Network)
	if err != nil {
		return "", "", err
	}
	srDoc := bson.D{
		{Key: "_id", Value: srID.String()},
		{Key: "agent_id", Value: sr.AgentID},
		{Key: "network", Value: sr.Network.String()},
		{Key: "challenge_hash", Value: sr.ChallengeHash},
		{Key: "verdict", Value: string(sr.Verdict)},
		{Key: "checks", Value: checksToBSON(sr.Checks)},
		{Key: "created_at", Value: now},
	}
	if sr.FailedRule != "" {
		srDoc = append(srDoc, bson.E{Key: "failed_rule", Value: sr.FailedRule})
	}
	if sr.RequirementsRaw != "" {
		srDoc = append(srDoc, bson.E{Key: "requirements_raw", Value: sr.RequirementsRaw})
	}
	if len(sr.Requirements) > 0 {
		srDoc = append(srDoc, bson.E{Key: "requirements", Value: sr.Requirements})
	}
	if len(sr.Warnings) > 0 {
		w := bson.A{}
		for _, x := range sr.Warnings {
			w = append(w, x)
		}
		srDoc = append(srDoc, bson.E{Key: "warnings", Value: w})
	}

	var payID ids.ID
	if p != nil {
		payID, err = ids.NewOn(ids.KindPayment, sr.Network)
		if err != nil {
			return "", "", err
		}
	}

	sess, err := s.client.StartSession()
	if err != nil {
		return "", "", classify(err)
	}
	defer sess.EndSession(ctx)

	_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		if _, err := s.db.Collection("sign_requests").InsertOne(sc, srDoc); err != nil {
			return nil, err
		}
		if p == nil {
			return nil, nil
		}
		_, err := s.db.Collection("payments").InsertOne(sc, bson.D{
			{Key: "_id", Value: payID.String()},
			{Key: "sign_request_id", Value: srID.String()},
			{Key: "agent_id", Value: p.AgentID},
			{Key: "agent_name", Value: p.AgentName},
			{Key: "allowance_id", Value: p.AllowanceID},
			{Key: "org_id", Value: p.OrgID},
			{Key: "network", Value: sr.Network.String()},
			{Key: "signature", Value: p.Signature},
			{Key: "memo", Value: p.Memo},
			{Key: "pay_to", Value: p.PayTo},
			{Key: "host", Value: p.Host},
			{Key: "amount_base", Value: int64(p.Amount)},
			{Key: "state", Value: string(state.PaymentSigned)},
			{Key: "state_at", Value: now},
			{Key: "created_at", Value: now},
		})
		return nil, err
	})
	if err != nil {
		return "", "", classify(err)
	}
	if p == nil {
		return srID.String(), "", nil
	}
	return srID.String(), payID.String(), nil
}

func checksToBSON(cs []policy.Check) bson.A {
	out := bson.A{}
	for _, c := range cs {
		out = append(out, bson.D{
			{Key: "rule", Value: c.Rule},
			{Key: "name", Value: c.Name},
			{Key: "tier", Value: string(c.Tier)},
			{Key: "result", Value: string(c.Result)},
			{Key: "detail", Value: c.Detail},
		})
	}
	return out
}

// PaymentsInFlight is what the sweep reads: everything whose outcome we have not seen.
func (s *Store) PaymentsInFlight(ctx context.Context, net network.Network,
	olderThan time.Time, limit int64) ([]Payment, error) {
	cur, err := s.db.Collection("payments").Find(ctx, bson.D{
		{Key: "network", Value: net.String()},
		{Key: "state", Value: bson.D{{Key: "$in", Value: bson.A{
			string(state.PaymentSigned), string(state.PaymentSubmitted), string(state.PaymentUnknown)}}}},
		{Key: "state_at", Value: bson.D{{Key: "$lt", Value: olderThan}}},
	}, options.Find().SetSort(bson.D{{Key: "state_at", Value: 1}}).SetLimit(limit))
	if err != nil {
		return nil, classify(err)
	}
	defer cur.Close(ctx)
	var docs []paymentDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, classify(err)
	}
	out := make([]Payment, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.toPayment())
	}
	return out, nil
}

// SetPaymentState moves a payment along, and refuses an illegal transition.
//
// `confirmed` requires a slot — the schema refuses one without — so there is no way to write it
// from a guess.
func (s *Store) SetPaymentState(ctx context.Context, net network.Network, paymentID string,
	st state.Payment, slot int64, now time.Time) error {
	set := bson.D{
		{Key: "state", Value: string(st)},
		{Key: "state_at", Value: now},
		{Key: "last_checked_at", Value: now},
	}
	if st == state.PaymentConfirmed {
		set = append(set, bson.E{Key: "confirmed_slot", Value: slot})
	}
	_, err := s.db.Collection("payments").UpdateOne(ctx,
		bson.D{{Key: "_id", Value: paymentID}, {Key: "network", Value: net.String()}},
		bson.D{{Key: "$set", Value: set}})
	return classify(err)
}

func (s *Store) ListPayments(ctx context.Context, orgID string, net network.Network, limit int64) ([]Payment, error) {
	cur, err := s.db.Collection("payments").Find(ctx,
		bson.D{{Key: "org_id", Value: orgID}, {Key: "network", Value: net.String()}},
		options.Find().SetSort(bson.D{{Key: "_id", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, classify(err)
	}
	defer cur.Close(ctx)
	var docs []paymentDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, classify(err)
	}
	out := make([]Payment, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.toPayment())
	}
	return out, nil
}

func (s *Store) GetPayment(ctx context.Context, paymentID string) (Payment, error) {
	var d paymentDoc
	if err := s.db.Collection("payments").FindOne(ctx,
		bson.D{{Key: "_id", Value: paymentID}}).Decode(&d); err != nil {
		return Payment{}, classify(err)
	}
	return d.toPayment(), nil
}

type paymentDoc struct {
	ID            string     `bson:"_id"`
	SignRequestID string     `bson:"sign_request_id"`
	AgentID       string     `bson:"agent_id"`
	AgentName     string     `bson:"agent_name"`
	AllowanceID   string     `bson:"allowance_id"`
	OrgID         string     `bson:"org_id"`
	Network       string     `bson:"network"`
	Signature     string     `bson:"signature,omitempty"`
	Memo          string     `bson:"memo,omitempty"`
	PayTo         string     `bson:"pay_to"`
	Host          string     `bson:"host"`
	Amount        money.Base `bson:"amount_base"`
	State         string     `bson:"state"`
	StateAt       time.Time  `bson:"state_at"`
	ConfirmedSlot *int64     `bson:"confirmed_slot,omitempty"`
	CreatedAt     time.Time  `bson:"created_at"`
}

func (d paymentDoc) toPayment() Payment {
	p := Payment{
		ID: d.ID, SignRequestID: d.SignRequestID, AgentID: d.AgentID, AgentName: d.AgentName,
		AllowanceID: d.AllowanceID, OrgID: d.OrgID, Network: network.Network(d.Network),
		Signature: d.Signature, Memo: d.Memo, PayTo: d.PayTo, Host: d.Host, Amount: d.Amount,
		State: state.Payment(d.State), StateAt: d.StateAt, CreatedAt: d.CreatedAt,
	}
	if d.ConfirmedSlot != nil {
		p.ConfirmedSlot = *d.ConfirmedSlot
	}
	return p
}

// ── The handshake timeline ───────────────────────────────────────────────────

// AppendPhase records one step of a payment's life.
//
// A duplicate is NOT an error to the caller: `one_phase_per_payment` refuses it, and that refusal
// is what makes every indexer job body safely re-runnable. A dropped tick or a retried iteration
// needs no reasoning about.
func (s *Store) AppendPhase(ctx context.Context, e HandshakeEvent) error {
	id, err := ids.New(ids.KindHandshake)
	if err != nil {
		return err
	}
	doc := bson.D{
		{Key: "_id", Value: id.String()},
		{Key: "payment_id", Value: e.PaymentID},
		{Key: "network", Value: e.Network.String()},
		{Key: "phase", Value: string(e.Phase)},
		{Key: "writer", Value: string(e.Writer)},
		{Key: "at", Value: e.At},
	}
	if e.ReadBackSlot != 0 {
		doc = append(doc, bson.E{Key: "read_back_slot", Value: e.ReadBackSlot})
	}
	if len(e.Detail) > 0 {
		doc = append(doc, bson.E{Key: "detail", Value: e.Detail})
	}
	if e.TraceID != "" {
		doc = append(doc, bson.E{Key: "trace_id", Value: e.TraceID})
	}
	_, err = s.db.Collection("handshake_events").InsertOne(ctx, doc)
	if err != nil && mongo.IsDuplicateKeyError(err) {
		return nil // already recorded; the phase happened once and is written once
	}
	return classify(err)
}

// Timeline returns the phases recorded for a payment, in order.
//
// A phase that has not happened is ABSENT rather than a placeholder. The interface renders absence
// differently for a blocked payment ("not reached") and one still in flight (a spinner), and
// fabricating a row for phase 4 — which happens inside the customer's agent and is unobservable —
// would be inventing evidence.
func (s *Store) Timeline(ctx context.Context, paymentID string) ([]HandshakeEvent, error) {
	cur, err := s.db.Collection("handshake_events").Find(ctx,
		bson.D{{Key: "payment_id", Value: paymentID}},
		options.Find().SetSort(bson.D{{Key: "at", Value: 1}}))
	if err != nil {
		return nil, classify(err)
	}
	defer cur.Close(ctx)
	var docs []handshakeDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, classify(err)
	}
	out := make([]HandshakeEvent, 0, len(docs))
	for _, d := range docs {
		e := HandshakeEvent{
			ID: d.ID, PaymentID: d.PaymentID, Network: network.Network(d.Network),
			Phase: state.Phase(d.Phase), Writer: state.Writer(d.Writer),
			Detail: d.Detail, TraceID: d.TraceID, At: d.At,
		}
		if d.ReadBackSlot != nil {
			e.ReadBackSlot = *d.ReadBackSlot
		}
		out = append(out, e)
	}
	// Sorted by the canonical phase order, not by timestamp.
	//
	// BSON dates are millisecond resolution and the signer writes three phases inside one
	// millisecond, so a time sort leaves their order to chance — and a timeline that sometimes
	// shows "rules evaluated" before "challenge received" is a timeline nobody can reconcile
	// against.
	sort.SliceStable(out, func(i, j int) bool {
		return phaseRank(out[i].Phase) < phaseRank(out[j].Phase)
	})
	return out, nil
}

func phaseRank(p state.Phase) int {
	for i, known := range state.PhaseOrder {
		if known == p {
			return i
		}
	}
	return len(state.PhaseOrder)
}

type handshakeDoc struct {
	ID           string         `bson:"_id"`
	PaymentID    string         `bson:"payment_id"`
	Network      string         `bson:"network"`
	Phase        string         `bson:"phase"`
	Writer       string         `bson:"writer"`
	ReadBackSlot *int64         `bson:"read_back_slot,omitempty"`
	Detail       map[string]any `bson:"detail,omitempty"`
	TraceID      string         `bson:"trace_id,omitempty"`
	At           time.Time      `bson:"at"`
}

var _ = errors.Is

// SetSettledSignature records the signature the chain actually settled under.
//
// For a sponsored payment that is the FEE PAYER's, and it is learned at read-back rather than at
// signing — so the audit trail names a transaction an explorer can show, instead of one that
// exists nowhere.
func (s *Store) SetSettledSignature(ctx context.Context, net network.Network,
	paymentID, signature string) error {
	_, err := s.db.Collection("payments").UpdateOne(ctx,
		bson.D{{Key: "_id", Value: paymentID}, {Key: "network", Value: net.String()}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "settled_signature", Value: signature}}}})
	return classify(err)
}
