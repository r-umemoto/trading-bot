package report_test

import (
	"context"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/r-umemoto/trading-bot/pkg/domain/report"
	reportinfra "github.com/r-umemoto/trading-bot/pkg/infra/report"
)

func TestFirestoreRepository_Save_ConnectionError(t *testing.T) {
	// Set the Firestore emulator env var to allow creating a client without credentials
	oldHost := os.Getenv("FIRESTORE_EMULATOR_HOST")
	os.Setenv("FIRESTORE_EMULATOR_HOST", "localhost:28989")
	defer func() {
		if oldHost == "" {
			os.Unsetenv("FIRESTORE_EMULATOR_HOST")
		} else {
			os.Setenv("FIRESTORE_EMULATOR_HOST", oldHost)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	client, err := firestore.NewClient(ctx, "test-project")
	if err != nil {
		t.Fatalf("failed to create dummy firestore client: %v", err)
	}
	defer client.Close()

	repo := reportinfra.NewFirestoreRepository(client)
	if repo == nil {
		t.Fatal("expected NewFirestoreRepository to return a non-nil repository")
	}

	testReport := &report.DailyReport{
		Date:      "2026-06-10",
		UpdatedAt: time.Now(),
	}

	// Save should fail because there is no emulator running at localhost:28989
	err = repo.Save(ctx, testReport)
	if err == nil {
		t.Fatal("expected Save to return an error when emulator is not reachable")
	}
}
