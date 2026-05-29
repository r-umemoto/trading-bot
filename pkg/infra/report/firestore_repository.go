package report

import (
	"context"

	"cloud.google.com/go/firestore"
	"github.com/r-umemoto/trading-bot/pkg/domain/report"
)

type FirestoreRepository struct {
	client *firestore.Client
}

func NewFirestoreRepository(client *firestore.Client) *FirestoreRepository {
	return &FirestoreRepository{client: client}
}

func (f *FirestoreRepository) Save(ctx context.Context, r *report.DailyReport) error {
	_, err := f.client.Collection("daily_reports").Doc(r.Date).Set(ctx, r)
	return err
}
