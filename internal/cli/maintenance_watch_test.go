package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
)

func TestMaintenanceWatchKeepsRetryIdentityAndMarksSixthFailedTickStale(t *testing.T) {
	f := newMaintenanceReportFixture(t, true)
	saveMaintenanceTestCredential(t, f)
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(start.Unix())
	oldNow := maintenanceNow
	maintenanceNow = func() time.Time { return time.Unix(clock.Load(), 0) }
	t.Cleanup(func() { maintenanceNow = oldNow })
	f.session.LastReportAt = start.Format(time.RFC3339)
	if err := kbstate.WriteMaintenance(f.root, f.session); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	reports := []license.MaintenanceReport{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body license.MaintenanceReport
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		reports = append(reports, body)
		n := len(reports)
		mu.Unlock()
		clock.Store(start.Add(time.Duration(n) * 5 * time.Minute).Unix())
		w.Header().Set("Content-Type", "application/json")
		if n <= 6 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"unavailable"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(license.ReportReply{AckSeq: body.Seq, ApprovalState: "redeemed", WorkUntil: start.Add(24 * time.Hour).Format(time.RFC3339)})
	}))
	defer server.Close()
	t.Setenv("KB_LICENSE_SERVER", server.URL)
	ticks := make(chan time.Time, 7)
	for i := 0; i < 7; i++ {
		ticks <- start.Add(time.Duration(i+1) * 5 * time.Minute)
	}
	close(ticks)
	var out bytes.Buffer
	if err := watchMaintenanceTicks(context.Background(), &out, ticks); err != nil {
		t.Fatal(err)
	}
	chunks := strings.Split(out.String(), "Report failed:")
	if len(chunks) != 7 {
		t.Fatalf("watch stopped during transient failures: %s", out.String())
	}
	for i := 1; i <= 5; i++ {
		if strings.Contains(chunks[i], "Stale:") {
			t.Fatalf("watch marked failure %d stale before thirty minutes", i)
		}
	}
	if !strings.Contains(chunks[6], "Stale: state unknown") {
		t.Fatal("sixth failed tick did not mark unknown state")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 8 {
		t.Fatalf("watch attempts %d", len(reports))
	}
	for i := 1; i < 7; i++ {
		if reports[i].Seq != reports[0].Seq || reports[i].PayloadSHA256 != reports[0].PayloadSHA256 {
			t.Fatal("watch retry changed sequence or payload identity")
		}
	}
	if reports[7].Seq != 2 {
		t.Fatalf("watch resumed with sequence %d", reports[7].Seq)
	}
	s, err := kbstate.ReadMaintenance(f.root)
	if err != nil || s.LastSeq != 2 || s.PendingReport != nil {
		t.Fatalf("watch acknowledgements: %+v %v", s, err)
	}
}
