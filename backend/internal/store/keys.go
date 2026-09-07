package store

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// WrappedKey reads an agent's encrypted seed and the reference to the key that wrapped it.
//
// It returns ciphertext. Nothing in this package can decrypt it, and nothing outside the signer
// process is permitted to try — `verify-architecture` asserts that only the signer reaches
// internal/kms.
func (s *Store) WrappedKey(ctx context.Context, agentID string) ([]byte, string, error) {
	var d struct {
		Wrapped   bson.Binary `bson:"wrapped_key"`
		KMSKeyRef string      `bson:"kms_key_ref"`
	}
	if err := s.db.Collection("agent_keys").FindOne(ctx,
		bson.D{{Key: "_id", Value: agentID}}).Decode(&d); err != nil {
		return nil, "", classify(err)
	}
	return d.Wrapped.Data, d.KMSKeyRef, nil
}

// RotateKey replaces an agent's key material and its lookup hash in one update.
//
// There is no overlap window: the old key stops working the moment this returns. A rotation is
// usually a response to a suspected leak, and a grace period would keep the leaked key working
// through exactly the minutes that matter.
func (s *Store) RotateKey(ctx context.Context, agentID string, wrapped []byte,
	keyRef, keyHash string) error {
	res, err := s.db.Collection("agent_keys").UpdateOne(ctx,
		bson.D{{Key: "_id", Value: agentID}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "wrapped_key", Value: bson.Binary{Subtype: 0x00, Data: wrapped}},
			{Key: "kms_key_ref", Value: keyRef},
			{Key: "key_hash", Value: keyHash},
			{Key: "rotated_at", Value: nowUTC()},
		}}})
	if err != nil {
		return classify(err)
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// WatchAgentKills calls back when an agent is killed, revoked or has its key rotated.
//
// This is what makes the kill switch's "instantly" true. Rule S7's promise — the sentence an owner
// reads before confirming — is that the signer refuses every new payment at once, and a 60-second
// cache TTL is not at once. A change stream evicts within milliseconds.
//
// It blocks until the stream fails or the context ends. The caller restarts it and falls back to
// the TTL in between, loudly: a degraded kill switch must be visible, not silent.
func (s *Store) WatchAgentKills(ctx context.Context, onChange func(agentID string)) error {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{
			{Key: "operationType", Value: bson.D{{Key: "$in", Value: bson.A{"update", "replace"}}}},
			{Key: "$or", Value: bson.A{
				bson.D{{Key: "updateDescription.updatedFields.killed", Value: bson.D{
					{Key: "$exists", Value: true}}}},
				bson.D{{Key: "operationType", Value: "replace"}},
			}},
		}}},
	}
	stream, err := s.db.Collection("agents").Watch(ctx, pipeline,
		options.ChangeStream().SetFullDocument(options.UpdateLookup))
	if err != nil {
		return fmt.Errorf("opening the kill-switch change stream: %w", err)
	}
	defer stream.Close(context.WithoutCancel(ctx))

	for stream.Next(ctx) {
		var ev struct {
			DocumentKey struct {
				ID string `bson:"_id"`
			} `bson:"documentKey"`
		}
		if err := stream.Decode(&ev); err != nil {
			continue
		}
		if ev.DocumentKey.ID != "" {
			onChange(ev.DocumentKey.ID)
		}
	}
	if err := stream.Err(); err != nil {
		return err
	}
	return ctx.Err()
}

// WatchHandshake streams handshake events for one payment, for the onboarding log.
//
// The specification is emphatic that onboarding step 5 must be REAL: one line per handshake row,
// streamed as it happens, never a scripted animation in the frontend. This is what makes that
// possible.
func (s *Store) WatchHandshake(ctx context.Context, paymentID string,
	onEvent func(HandshakeEvent)) error {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{
			{Key: "operationType", Value: "insert"},
			{Key: "fullDocument.payment_id", Value: paymentID},
		}}},
	}
	stream, err := s.db.Collection("handshake_events").Watch(ctx, pipeline)
	if err != nil {
		return fmt.Errorf("opening the handshake change stream: %w", err)
	}
	defer stream.Close(context.WithoutCancel(ctx))

	for stream.Next(ctx) {
		var ev struct {
			Full handshakeDoc `bson:"fullDocument"`
		}
		if err := stream.Decode(&ev); err != nil {
			continue
		}
		e := HandshakeEvent{
			ID: ev.Full.ID, PaymentID: ev.Full.PaymentID,
			Phase: phaseOf(ev.Full.Phase), Writer: writerOf(ev.Full.Writer),
			Detail: ev.Full.Detail, At: ev.Full.At,
		}
		if ev.Full.ReadBackSlot != nil {
			e.ReadBackSlot = *ev.Full.ReadBackSlot
		}
		onEvent(e)
	}
	if err := stream.Err(); err != nil {
		return err
	}
	return ctx.Err()
}

// PutAgentKey stores an agent's wrapped key for the first time.
//
// Called only by the signer, and only at provisioning. There is no update path here — replacing a
// key is RotateKey, which is a different operation with a different meaning.
func (s *Store) PutAgentKey(ctx context.Context, agentID string, wrapped []byte,
	keyRef, keyHash string, now time.Time) error {
	_, err := s.db.Collection("agent_keys").InsertOne(ctx, bson.D{
		{Key: "_id", Value: agentID},
		{Key: "kms_key_ref", Value: keyRef},
		{Key: "wrapped_key", Value: bson.Binary{Subtype: 0x00, Data: wrapped}},
		{Key: "key_hash", Value: keyHash},
		{Key: "created_at", Value: now},
	})
	return classify(err)
}

// DeleteAgent removes an agent that was never completed.
//
// It exists for exactly one case: the dashboard created the record and then failed to provision a
// key, leaving an agent the signer would refuse in a way nobody could diagnose. It refuses to
// touch an agent that has ever signed anything, because those records are append-only and an agent
// with history is not a mistake to clean up.
func (s *Store) DeleteAgent(ctx context.Context, agentID string) error {
	n, err := s.db.Collection("sign_requests").CountDocuments(ctx,
		bson.D{{Key: "agent_id", Value: agentID}})
	if err != nil {
		return classify(err)
	}
	if n > 0 {
		return fmt.Errorf("agent %s has %d sign requests and cannot be deleted", agentID, n)
	}
	for _, coll := range []string{"policies", "agent_keys", "agents"} {
		if _, err := s.db.Collection(coll).DeleteOne(ctx,
			bson.D{{Key: "_id", Value: agentID}}); err != nil {
			return classify(err)
		}
	}
	return nil
}
