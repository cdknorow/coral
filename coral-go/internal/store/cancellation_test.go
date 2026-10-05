package store

import (
	"context"
	"math/rand"
	"testing"
	"time"
)

// Exercise cancellation around statement completion: older modernc drivers
// could interrupt a connection after returning it to our single-connection pool,
// permanently breaking subsequent requests. This is a bounded race stress test,
// not a guarantee that every run will reproduce the old driver defect.
func TestCancellationLeavesPoolUsable(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TABLE cancellation_probe(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<200) INSERT INTO cancellation_probe SELECT CAST(x AS TEXT) FROM n`); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Duration(rng.Intn(60))*time.Microsecond, cancel)
		var count int
		_ = db.QueryRowContext(ctx, `SELECT count(*) FROM cancellation_probe WHERE v LIKE '%1%'`).Scan(&count)
		timer.Stop()
		cancel()
		// A fresh request must still see the persisted data, even if the pool
		// discarded an interrupted connection and opened another one.
		if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM cancellation_probe`).Scan(&count); err != nil {
			t.Fatalf("fresh request after cancellation %d: %v", i, err)
		}
		if count != 200 {
			t.Fatalf("persisted rows after cancellation %d: got %d", i, count)
		}
	}
	if _, err := db.Exec(`INSERT INTO cancellation_probe VALUES ('still writable')`); err != nil {
		t.Fatal(err)
	}
}
