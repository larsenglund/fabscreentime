package server

import (
	"testing"
	"time"
)

type fakeNotifier struct{ calls []string }

func (f *fakeNotifier) Send(title, message, _ string) {
	f.calls = append(f.calls, title+": "+message)
}

func setLastSeen(t *testing.T, st *Store, uuid string, ts int64) {
	t.Helper()
	if _, err := st.db.Exec(`UPDATE devices SET last_seen=? WHERE device_uuid=?`, ts, uuid); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineAlertsTransitionOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, st := newTestServer(t, now)
	fake := &fakeNotifier{}
	srv.notifier = fake
	srv.offlineAfter = 15 * time.Minute
	h := srv.Handler()

	// Device A is already stale at seed time; device B is fresh.
	a, _ := enrollDevice(t, h, "A")
	b, _ := enrollDevice(t, h, "B")
	setLastSeen(t, st, a, now.Add(-30*time.Minute).Unix())

	// First pass seeds (A offline) without alerting.
	srv.checkOffline(now.Unix())
	if len(fake.calls) != 0 {
		t.Fatalf("seed pass must not alert, got %v", fake.calls)
	}

	// B is still online → no alert.
	srv.checkOffline(now.Unix())
	if len(fake.calls) != 0 {
		t.Fatalf("online device must not alert, got %v", fake.calls)
	}

	// B goes silent → exactly one offline alert (A was already seeded).
	setLastSeen(t, st, b, now.Add(-20*time.Minute).Unix())
	srv.checkOffline(now.Unix())
	if len(fake.calls) != 1 {
		t.Fatalf("offline transition should alert once, got %d: %v", len(fake.calls), fake.calls)
	}

	// Re-check with no change → no duplicate.
	srv.checkOffline(now.Unix())
	if len(fake.calls) != 1 {
		t.Fatalf("must not re-alert a still-offline device, got %d", len(fake.calls))
	}

	// B recovers → a "back online" alert, and the state clears.
	setLastSeen(t, st, b, now.Unix())
	srv.checkOffline(now.Unix())
	if len(fake.calls) != 2 {
		t.Fatalf("recovery should alert, got %d: %v", len(fake.calls), fake.calls)
	}
}

func TestSeededOfflineDeviceNoSpuriousRecovery(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, st := newTestServer(t, now)
	fake := &fakeNotifier{}
	srv.notifier = fake
	srv.offlineAfter = 15 * time.Minute
	h := srv.Handler()

	a, _ := enrollDevice(t, h, "A")
	setLastSeen(t, st, a, now.Add(-30*time.Minute).Unix()) // offline at seed time

	srv.checkOffline(now.Unix()) // seed: A offline but no alert
	// A comes back — we never announced it offline, so no "back online".
	setLastSeen(t, st, a, now.Unix())
	srv.checkOffline(now.Unix())
	if len(fake.calls) != 0 {
		t.Fatalf("a seeded (never-alerted) device must not announce recovery, got %v", fake.calls)
	}
}

func TestAlertedEntryPrunedWhenDeviceDeleted(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	srv, st := newTestServer(t, now)
	srv.notifier = &fakeNotifier{}
	srv.offlineAfter = 15 * time.Minute
	h := srv.Handler()

	a, _ := enrollDevice(t, h, "A")
	srv.checkOffline(now.Unix()) // seed (A online)
	setLastSeen(t, st, a, now.Add(-20*time.Minute).Unix())
	srv.checkOffline(now.Unix()) // A offline → alerted
	if len(srv.alerted) != 1 {
		t.Fatalf("expected A tracked as alerted, got %d", len(srv.alerted))
	}
	if _, err := st.DeleteDevice(a); err != nil {
		t.Fatal(err)
	}
	srv.checkOffline(now.Unix()) // A gone → pruned, no leak
	if len(srv.alerted) != 0 {
		t.Fatalf("alerted map should be pruned after delete, got %d", len(srv.alerted))
	}
}
