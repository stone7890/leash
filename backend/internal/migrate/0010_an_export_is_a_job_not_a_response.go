package migrate

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// An export is a job, not a response.
//
// The audit export always returns 202. A wide date range over months of payments does not fit in a
// request, and pretending otherwise produces a timeout at exactly the moment a customer is trying
// to satisfy an auditor. The interface says so: "the button queues it, this list shows it when
// it's ready".
//
// The same collection carries the test-payment jobs from onboarding step 5, because they are the
// same shape: something queued, watched, and eventually finished.
func init() {
	register(Migration{
		Version: 10,
		Name:    "an_export_is_a_job_not_a_response",
		Up: func(ctx context.Context, db *mongo.Database) error {
			jobs := validator(schema(
				[]string{"_id", "org_id", "kind", "status", "created_at"},
				bson.D{
					{Key: "_id", Value: pattern(idPattern("job"))},
					{Key: "org_id", Value: pattern(idPattern("org"))},
					{Key: "kind", Value: enum("audit_export", "test_payment", "faucet")},
					{Key: "status", Value: enum("queued", "running", "ready", "failed")},
					{Key: "network", Value: enum("sandbox", "mainnet")},
					// Dates as YYYY-MM-DD strings: an export range is a calendar range chosen by a
					// person, not an instant, and storing it as a timestamp invites a timezone bug
					// at the boundary of the very rows an auditor is checking.
					{Key: "range_from", Value: pattern(`^\d{4}-\d{2}-\d{2}$`)},
					{Key: "range_to", Value: pattern(`^\d{4}-\d{2}-\d{2}$`)},
					{Key: "agent_id", Value: nullableStr()},
					{Key: "payment_id", Value: nullableStr()},
					{Key: "url", Value: nullableStr()},
					{Key: "error", Value: nullableStr()},
					{Key: "idempotency_key", Value: str(8, 128)},
					{Key: "row_count", Value: nullableLong()},
					{Key: "expires_at", Value: nullableDate()},
					{Key: "created_at", Value: date()},
					{Key: "finished_at", Value: nullableDate()},
				}))
			if err := createCollection(ctx, db, "audit_jobs", jobs); err != nil {
				return err
			}
			return createIndexes(ctx, db, "audit_jobs", []mongo.IndexModel{
				{
					Keys:    bson.D{{Key: "org_id", Value: 1}, {Key: "_id", Value: -1}},
					Options: options.Index().SetName("jobs_newest_first"),
				},
				{
					Keys:    bson.D{{Key: "idempotency_key", Value: 1}},
					Options: options.Index().SetUnique(true).SetName("one_job_per_idempotency_key"),
				},
				{
					Keys:    bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: 1}},
					Options: options.Index().SetName("jobs_to_run"),
				},
			})
		},
		Down: func(ctx context.Context, db *mongo.Database) error {
			return dropCollection(ctx, db, "audit_jobs")
		},
	})
}
