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
//
// The driver discards an interrupted connection, so about one cancelled query in six makes
// the pool open a new one, which re-reads the schema. That is under a millisecond normally
// but slow under -race, where 20000 rounds ran past the package timeout (#3557). The loop is
// bounded by time as well, and must still cancel enough queries to mean something.
func TestCancellationLeavesPoolUsable(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`CREATE TABLE cancellation_probe(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<200) INSERT INTO cancellation_probe SELECT CAST(x AS TEXT) FROM n`); err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	deadline := time.Now().Add(15 * time.Second)
	const minCancelled = 20
	cancelled, rounds := 0, 0
	// A timer that fires within 60 us is a coin flip on a runner whose timers tick every millisecond or more (Windows: 17 of 20000 rounds, #5354 gate), so the loop goes on past
	// 20000 rounds, up to the deadline, until enough queries were cancelled.
	for i := 0; (i < 20000 || cancelled < minCancelled) && time.Now().Before(deadline); i++ {
		rounds++
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Duration(rng.Intn(60))*time.Microsecond, cancel)
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM cancellation_probe WHERE v LIKE '%1%'`).Scan(&count); err != nil {
			cancelled++
		}
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
	t.Logf("%d rounds, %d queries cancelled", rounds, cancelled)
	if cancelled < minCancelled {
		t.Fatalf("only %d queries were cancelled; the test exercised too little", cancelled)
	}
	if _, err := db.Exec(`INSERT INTO cancellation_probe VALUES ('still writable')`); err != nil {
		t.Fatal(err)
	}
}
