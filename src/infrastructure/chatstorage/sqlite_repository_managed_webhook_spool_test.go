package chatstorage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	domainSpool "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/webhookspool"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/sqlite"
)

func managedEnqueueRequest(id string) *domainSpool.EnqueueRequest {
	return &domainSpool.EnqueueRequest{
		DeliveryID:          id,
		DeviceDigest:        "dev-digest",
		SourceSessionDigest: "session-digest",
		EventName:           "message",
		MessageIDDigest:     "message-digest-" + id,
		BodyHash:            "body-hash-" + id,
		PayloadCiphertext:   []byte("ciphertext"),
		PayloadKeyVersion:   "payload-key-v3",
		SecretVersion:       "webhook-secret-v7",
		MaxAttempts:         5,
		MaxAge:              time.Hour,
	}
}

func TestManagedWebhookSpoolEnqueueIsIdempotentAndClaimsWithFence(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	req := &domainSpool.EnqueueRequest{
		DeliveryID:          "delivery-opaque-1",
		DeviceDigest:        "dev-digest",
		SourceSessionDigest: "session-digest",
		EventName:           "message",
		MessageIDDigest:     "message-digest",
		BodyHash:            "body-hash",
		PayloadCiphertext:   []byte("ciphertext"),
		PayloadKeyVersion:   "payload-key-v3",
		SecretVersion:       "webhook-secret-v7",
		MaxAttempts:         5,
		MaxAge:              time.Until(now.Add(time.Hour)),
	}

	first, created, err := repo.EnqueueManagedWebhookDelivery(ctx, req)
	if err != nil {
		t.Fatalf("enqueue first delivery: %v", err)
	}
	if !created || first == nil {
		t.Fatalf("expected newly created delivery, got created=%v delivery=%+v", created, first)
	}

	second, created, err := repo.EnqueueManagedWebhookDelivery(ctx, req)
	if err != nil {
		t.Fatalf("enqueue duplicate delivery: %v", err)
	}
	if created || second.ID != first.ID || second.DeliveryID != first.DeliveryID {
		t.Fatalf("expected idempotent existing delivery, first=%+v second=%+v created=%v", first, second, created)
	}
	conflict := *req
	conflict.DeliveryID = "delivery-opaque-conflict"
	if _, _, err := repo.EnqueueManagedWebhookDelivery(ctx, &conflict); err == nil {
		t.Fatal("mismatched delivery id reused an existing protected identity")
	}

	claim, err := repo.ClaimManagedWebhookDelivery(ctx, "worker-a", 30*time.Second)
	if err != nil {
		t.Fatalf("claim delivery: %v", err)
	}
	if claim == nil || claim.Status != domainSpool.StatusProcessing || claim.Owner != "worker-a" {
		t.Fatalf("unexpected claim: %+v", claim)
	}
	if claim.AttemptCount != 1 || claim.FenceToken != 1 {
		t.Fatalf("expected first attempt/fence, got attempt=%d fence=%d", claim.AttemptCount, claim.FenceToken)
	}

	other, err := repo.ClaimManagedWebhookDelivery(ctx, "worker-b", 30*time.Second)
	if err != nil {
		t.Fatalf("concurrent claim: %v", err)
	}
	if other != nil {
		t.Fatalf("expected active lease to prevent second claim, got %+v", other)
	}
}

func TestManagedWebhookSpoolExpiredLeaseTakeoverRejectsStaleFence(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	ctx := context.Background()
	_, _, err := repo.EnqueueManagedWebhookDelivery(ctx, &domainSpool.EnqueueRequest{
		DeliveryID:          "delivery-opaque-2",
		DeviceDigest:        "dev-digest",
		SourceSessionDigest: "session-digest",
		EventName:           "message",
		MessageIDDigest:     "message-digest",
		BodyHash:            "body-hash-2",
		PayloadCiphertext:   []byte("ciphertext"),
		PayloadKeyVersion:   "payload-key-v3",
		SecretVersion:       "webhook-secret-v7",
		MaxAttempts:         5,
		MaxAge:              time.Hour,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	first, err := repo.ClaimManagedWebhookDelivery(ctx, "worker-a", time.Second)
	if err != nil || first == nil {
		t.Fatalf("first claim: delivery=%+v err=%v", first, err)
	}
	if _, err := repo.db.Exec(`UPDATE managed_webhook_spool SET lease_until = datetime('now', '-1 second') WHERE id = ?`, first.ID); err != nil {
		t.Fatalf("expire lease using DB clock: %v", err)
	}

	second, err := repo.ClaimManagedWebhookDelivery(ctx, "worker-b", 30*time.Second)
	if err != nil || second == nil {
		t.Fatalf("takeover claim: delivery=%+v err=%v", second, err)
	}
	if second.FenceToken <= first.FenceToken || second.AttemptCount != 2 {
		t.Fatalf("takeover did not advance fence/attempt: first=%+v second=%+v", first, second)
	}

	updated, err := repo.CompleteManagedWebhookDelivery(ctx, first.ID, first.Owner, first.FenceToken)
	if err != nil {
		t.Fatalf("stale completion returned storage error: %v", err)
	}
	if updated {
		t.Fatal("stale owner/fence completed a delivery after takeover")
	}

	updated, err = repo.CompleteManagedWebhookDelivery(ctx, second.ID, second.Owner, second.FenceToken)
	if err != nil || !updated {
		t.Fatalf("current owner/fence could not complete: updated=%v err=%v", updated, err)
	}
}

func TestManagedWebhookSpoolConcurrentWorkersClaimExactlyOnce(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	ctx := context.Background()
	if _, _, err := repo.EnqueueManagedWebhookDelivery(ctx, managedEnqueueRequest("concurrent")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	start := make(chan struct{})
	results := make(chan *domainSpool.Delivery, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, owner := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			<-start
			claim, err := repo.ClaimManagedWebhookDelivery(ctx, owner, time.Minute)
			results <- claim
			errs <- err
		}(owner)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent claim returned error: %v", err)
		}
	}
	claimed := 0
	for delivery := range results {
		if delivery != nil {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("expected exactly one active claim, got %d", claimed)
	}
}

func TestManagedWebhookSpoolClaimRetriesTransientSQLiteContention(t *testing.T) {
	path := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "claim-retry.db"))
	dsn := sqlite.FormatChatStorageURI(path, true, true)

	lockDB, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatalf("open lock database: %v", err)
	}
	defer lockDB.Close()
	lockDB.SetMaxOpenConns(1)
	lockDB.SetMaxIdleConns(1)
	lockRepo := &SQLiteRepository{db: lockDB}
	if err := lockRepo.InitializeSchema(); err != nil {
		t.Fatalf("initialize lock database: %v", err)
	}
	if _, _, err := lockRepo.EnqueueManagedWebhookDelivery(context.Background(), managedEnqueueRequest("claim-retry")); err != nil {
		t.Fatalf("enqueue before contention: %v", err)
	}

	claimDB, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatalf("open claim database: %v", err)
	}
	defer claimDB.Close()
	claimDB.SetMaxOpenConns(1)
	claimDB.SetMaxIdleConns(1)
	if _, err := claimDB.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatalf("disable driver wait to exercise repository retry: %v", err)
	}
	claimRepo := &SQLiteRepository{db: claimDB}

	lockConn, err := lockDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("reserve lock connection: %v", err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("acquire SQLite write lock: %v", err)
	}
	released := make(chan error, 1)
	go func() {
		time.Sleep(12 * time.Millisecond)
		_, releaseErr := lockConn.ExecContext(context.Background(), "ROLLBACK")
		released <- releaseErr
	}()

	claim, err := claimRepo.ClaimManagedWebhookDelivery(context.Background(), "worker-after-busy", time.Minute)
	if releaseErr := <-released; releaseErr != nil {
		t.Fatalf("release SQLite write lock: %v", releaseErr)
	}
	if err != nil || claim == nil {
		t.Fatalf("transient contention escaped bounded claim retry: delivery=%+v err=%v", claim, err)
	}
	if claim.DeliveryID != "claim-retry" || claim.Owner != "worker-after-busy" || claim.AttemptCount != 1 || claim.FenceToken != 1 {
		t.Fatalf("retry changed the atomic DB-clock claim transition: %+v", claim)
	}
}

func TestManagedWebhookSpoolClaimBusyRetryIsBounded(t *testing.T) {
	path := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "claim-bounded.db"))
	dsn := sqlite.FormatChatStorageURI(path, true, true)
	lockDB, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatalf("open lock database: %v", err)
	}
	defer lockDB.Close()
	lockDB.SetMaxOpenConns(1)
	lockDB.SetMaxIdleConns(1)
	lockRepo := &SQLiteRepository{db: lockDB}
	if err := lockRepo.InitializeSchema(); err != nil {
		t.Fatalf("initialize lock database: %v", err)
	}
	if _, _, err := lockRepo.EnqueueManagedWebhookDelivery(context.Background(), managedEnqueueRequest("claim-bounded")); err != nil {
		t.Fatalf("enqueue before persistent contention: %v", err)
	}

	claimDB, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatalf("open claim database: %v", err)
	}
	defer claimDB.Close()
	claimDB.SetMaxOpenConns(1)
	claimDB.SetMaxIdleConns(1)
	if _, err := claimDB.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatalf("disable driver wait to exercise bounded retry: %v", err)
	}
	claimRepo := &SQLiteRepository{db: claimDB}

	lockConn, err := lockDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("reserve lock connection: %v", err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("acquire SQLite write lock: %v", err)
	}
	defer lockConn.ExecContext(context.Background(), "ROLLBACK")

	started := time.Now()
	claim, err := claimRepo.ClaimManagedWebhookDelivery(context.Background(), "worker-bounded", time.Minute)
	if claim != nil || !errors.Is(err, domainSpool.ErrRepositoryBusy) {
		t.Fatalf("persistent contention did not return the typed transient error: delivery=%+v err=%v", claim, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("claim contention retry was not bounded: elapsed=%s", elapsed)
	}
}

func TestManagedWebhookSpoolProductionDSNConcurrentClaimStress(t *testing.T) {
	const deliveries = 128
	path := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "claim-stress.db"))
	dsn := sqlite.FormatChatStorageURI(path, true, true)
	db, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatalf("open production-style SQLite database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)
	repo := &SQLiteRepository{db: db}
	if err := repo.InitializeSchema(); err != nil {
		t.Fatalf("initialize production-style SQLite database: %v", err)
	}
	for i := 0; i < deliveries; i++ {
		id := fmt.Sprintf("stress-%03d", i)
		if _, _, err := repo.EnqueueManagedWebhookDelivery(context.Background(), managedEnqueueRequest(id)); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}

	start := make(chan struct{})
	results := make(chan *domainSpool.Delivery, deliveries)
	errs := make(chan error, deliveries)
	var wg sync.WaitGroup
	for i := 0; i < deliveries; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			claim, claimErr := repo.ClaimManagedWebhookDelivery(context.Background(), fmt.Sprintf("stress-worker-%03d", worker), time.Minute)
			results <- claim
			errs <- claimErr
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("production-DSN stress claim returned error: %v", err)
		}
	}
	claimed := make(map[int64]struct{}, deliveries)
	for claim := range results {
		if claim == nil {
			t.Fatal("production-DSN stress lost a due claim")
		}
		if _, duplicate := claimed[claim.ID]; duplicate {
			t.Fatalf("production-DSN stress claimed row %d more than once", claim.ID)
		}
		claimed[claim.ID] = struct{}{}
	}
	if len(claimed) != deliveries {
		t.Fatalf("production-DSN stress claimed %d/%d rows", len(claimed), deliveries)
	}
}

func TestManagedWebhookSpoolSurvivesRepositoryRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	openRepo := func() (*sql.DB, *SQLiteRepository) {
		db, err := sql.Open(sqlite.DriverName, path)
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		repo := &SQLiteRepository{db: db}
		if err := repo.InitializeSchema(); err != nil {
			_ = db.Close()
			t.Fatalf("initialize schema: %v", err)
		}
		return db, repo
	}

	db, first := openRepo()
	if _, _, err := first.EnqueueManagedWebhookDelivery(context.Background(), managedEnqueueRequest("restart")); err != nil {
		t.Fatalf("enqueue before restart: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}

	db, second := openRepo()
	defer db.Close()
	claim, err := second.ClaimManagedWebhookDelivery(context.Background(), "worker-after-restart", time.Minute)
	if err != nil || claim == nil || claim.DeliveryID != "restart" {
		t.Fatalf("durable claim after restart: delivery=%+v err=%v", claim, err)
	}
}

func TestManagedWebhookSpoolStorageDownFailsAdmission(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	if err := repo.db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	if _, _, err := repo.EnqueueManagedWebhookDelivery(context.Background(), managedEnqueueRequest("db-down")); err == nil {
		t.Fatal("storage failure was reported as successful durable admission")
	}
}

func TestManagedWebhookSpoolBusyDatabaseFailsAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	db1, err := sql.Open(sqlite.DriverName, path+"?_pragma=busy_timeout(50)")
	if err != nil {
		t.Fatalf("open lock database: %v", err)
	}
	defer db1.Close()
	repo1 := &SQLiteRepository{db: db1}
	if err := repo1.InitializeSchema(); err != nil {
		t.Fatalf("initialize lock database: %v", err)
	}
	lockConn, err := db1.Conn(context.Background())
	if err != nil {
		t.Fatalf("reserve lock connection: %v", err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("acquire exclusive SQLite lock: %v", err)
	}
	defer lockConn.ExecContext(context.Background(), "ROLLBACK")

	db2, err := sql.Open(sqlite.DriverName, path+"?_pragma=busy_timeout(50)")
	if err != nil {
		t.Fatalf("open admission database: %v", err)
	}
	defer db2.Close()
	repo2 := &SQLiteRepository{db: db2}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, _, err := repo2.EnqueueManagedWebhookDelivery(ctx, managedEnqueueRequest("db-busy")); err == nil {
		t.Fatal("busy SQLite writer was reported as successful durable admission")
	}
}

func TestManagedWebhookSpoolExpiredBudgetBecomesDead(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	ctx := context.Background()
	if _, _, err := repo.EnqueueManagedWebhookDelivery(ctx, managedEnqueueRequest("expired")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := repo.db.Exec(`UPDATE managed_webhook_spool SET deadline_at = datetime('now', '-1 second') WHERE delivery_id = ?`, "expired"); err != nil {
		t.Fatalf("expire delivery using DB clock: %v", err)
	}
	claim, err := repo.ClaimManagedWebhookDelivery(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("claim after expiry: %v", err)
	}
	if claim != nil {
		t.Fatalf("expired delivery was claimed: %+v", claim)
	}
	var status, code string
	if err := repo.db.QueryRow(`SELECT status, last_error_code FROM managed_webhook_spool WHERE delivery_id = ?`, "expired").Scan(&status, &code); err != nil {
		t.Fatalf("read expired delivery: %v", err)
	}
	if status != string(domainSpool.StatusDead) || code != "deadline_exhausted" {
		t.Fatalf("expired delivery did not become visible dead-letter: status=%q code=%q", status, code)
	}
}

func TestManagedWebhookSpoolMissingCiphertextBecomesDead(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	ctx := context.Background()
	if _, _, err := repo.EnqueueManagedWebhookDelivery(ctx, managedEnqueueRequest("missing-ciphertext")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := repo.db.Exec(`UPDATE managed_webhook_spool SET payload_ciphertext = NULL WHERE delivery_id = ?`, "missing-ciphertext"); err != nil {
		t.Fatalf("corrupt stored ciphertext: %v", err)
	}
	claim, err := repo.ClaimManagedWebhookDelivery(ctx, "worker-a", time.Minute)
	if err != nil || claim != nil {
		t.Fatalf("corrupt delivery claim: delivery=%+v err=%v", claim, err)
	}
	var status, code string
	if err := repo.db.QueryRow(`SELECT status, last_error_code FROM managed_webhook_spool WHERE delivery_id = ?`, "missing-ciphertext").Scan(&status, &code); err != nil {
		t.Fatalf("read corrupt delivery: %v", err)
	}
	if status != string(domainSpool.StatusDead) || code != "ciphertext_invalid" {
		t.Fatalf("corrupt delivery did not become visible dead-letter: status=%q code=%q", status, code)
	}
}

func TestManagedWebhookSpoolDeadLetterIsQueryableAndRetentionKeepsTombstone(t *testing.T) {
	repo := newTestSQLiteRepository(t)
	ctx := context.Background()
	req := managedEnqueueRequest("dead-retention")
	if _, _, err := repo.EnqueueManagedWebhookDelivery(ctx, req); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claim, err := repo.ClaimManagedWebhookDelivery(ctx, "worker-a", time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim: delivery=%+v err=%v", claim, err)
	}
	if updated, err := repo.DeadLetterManagedWebhookDelivery(ctx, claim.ID, claim.Owner, claim.FenceToken, "http_client_error"); err != nil || !updated {
		t.Fatalf("dead-letter: updated=%v err=%v", updated, err)
	}
	dead, err := repo.ListManagedWebhookDeadLetters(ctx, 10)
	if err != nil || len(dead) != 1 || dead[0].LastErrorCode != "http_client_error" {
		t.Fatalf("query dead-letter: rows=%+v err=%v", dead, err)
	}
	if _, err := repo.db.Exec(`UPDATE managed_webhook_spool SET dead_at = datetime('now', '-2 hours') WHERE id = ?`, claim.ID); err != nil {
		t.Fatalf("age terminal row: %v", err)
	}
	purged, err := repo.PurgeTerminalManagedWebhookPayloads(ctx, time.Now().UTC().Add(-time.Hour), 10)
	if err != nil || purged != 1 {
		t.Fatalf("purge terminal ciphertext: purged=%d err=%v", purged, err)
	}
	row, err := repo.GetManagedWebhookDelivery(ctx, claim.ID)
	if err != nil || row == nil || row.PayloadCiphertext != nil || row.PayloadPurgedAt == nil || row.Status != domainSpool.StatusDead {
		t.Fatalf("retention did not preserve terminal tombstone: row=%+v err=%v", row, err)
	}
	duplicate, created, err := repo.EnqueueManagedWebhookDelivery(ctx, req)
	if err != nil || created || duplicate == nil || duplicate.ID != claim.ID {
		t.Fatalf("purged replay bypassed tombstone: delivery=%+v created=%v err=%v", duplicate, created, err)
	}
}
