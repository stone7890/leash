package store

import (
	"context"
	"time"

	"github.com/stone7890/leash/internal/domain/ids"
	"github.com/stone7890/leash/internal/domain/network"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Alert struct {
	ID        string
	OrgID     string
	AgentID   string
	Network   network.Network
	Kind      string
	Body      map[string]any
	DedupeKey string
	SentAt    time.Time
	CreatedAt time.Time
}

// RaiseAlert records one, and does nothing if the same situation has already been raised.
//
// The dedupe key is what makes a 30-second evaluation loop bearable: a crossed budget threshold
// STAYS crossed, and without this an owner would be told about it twice a minute until they acted.
// A duplicate is not an error — it is the normal case.
func (s *Store) RaiseAlert(ctx context.Context, a Alert) error {
	id, err := ids.New(ids.KindAlert)
	if err != nil {
		return err
	}
	doc := bson.D{
		{Key: "_id", Value: id.String()},
		{Key: "org_id", Value: a.OrgID},
		{Key: "network", Value: a.Network.String()},
		{Key: "kind", Value: a.Kind},
		{Key: "body", Value: a.Body},
		{Key: "dedupe_key", Value: a.DedupeKey},
		{Key: "created_at", Value: a.CreatedAt},
	}
	if a.AgentID != "" {
		doc = append(doc, bson.E{Key: "agent_id", Value: a.AgentID})
	}
	_, err = s.db.Collection("alerts").InsertOne(ctx, doc)
	if err != nil && mongo.IsDuplicateKeyError(err) {
		return nil
	}
	return classify(err)
}

func (s *Store) ListAlerts(ctx context.Context, orgID string, limit int64) ([]Alert, error) {
	cur, err := s.db.Collection("alerts").Find(ctx,
		bson.D{{Key: "org_id", Value: orgID}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, classify(err)
	}
	defer cur.Close(ctx)
	var docs []struct {
		ID        string         `bson:"_id"`
		OrgID     string         `bson:"org_id"`
		AgentID   string         `bson:"agent_id,omitempty"`
		Network   string         `bson:"network"`
		Kind      string         `bson:"kind"`
		Body      map[string]any `bson:"body"`
		CreatedAt time.Time      `bson:"created_at"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, classify(err)
	}
	out := make([]Alert, 0, len(docs))
	for _, d := range docs {
		out = append(out, Alert{
			ID: d.ID, OrgID: d.OrgID, AgentID: d.AgentID,
			Network: network.Network(d.Network), Kind: d.Kind, Body: d.Body,
			CreatedAt: d.CreatedAt,
		})
	}
	return out, nil
}
