package license

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestMaintenanceRequestHeadersAndReply(t *testing.T) {
	const token = "bearer-secret"
	var gotPath, gotAuth, gotSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotSession = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get(maintenanceCredentialHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"` + strings.Repeat("a", 32) + `","session_id":"` + strings.Repeat("b", 32) + `","status":"requested","approve_by":"2030-01-01T00:00:00Z","apps":["kb_pro"]}`))
	}))
	defer srv.Close()
	reply, err := RequestMaintenance(context.Background(), srv.URL, token, MaintenanceRequest{ClientRequestID: strings.Repeat("a", 32), BenchID: strings.Repeat("b", 32), Fingerprint: strings.Repeat("c", 64), LicenseKeySHA256: strings.Repeat("d", 64), Apps: []string{"kb_pro"}, Reason: "repair"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/maintenance/request" || gotAuth != "Bearer "+token || gotSession != "" {
		t.Fatalf("request path/auth/session = %q %q %q", gotPath, gotAuth, gotSession)
	}
	if reply.RequestID != strings.Repeat("a", 32) || reply.Status != "requested" || len(reply.Apps) != 1 {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestMaintenanceReportUsesCredentialHeaderAndDoesNotExposeBody(t *testing.T) {
	const credential = "secret-maintenance-credential"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(maintenanceCredentialHeader) != credential {
			t.Errorf("credential header = %q", r.Header.Get(maintenanceCredentialHeader))
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header")
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"` + credential + `"}`))
	}))
	defer srv.Close()
	_, err := ReportMaintenance(context.Background(), srv.URL, credential, MaintenanceReport{Seq: 1})
	if err == nil || strings.Contains(err.Error(), credential) || strings.Contains(err.Error(), `{"error"`) {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestMaintenanceDoesNotFollowRedirects(t *testing.T) {
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer srv.Close()
	_, err := RequestMaintenance(context.Background(), srv.URL, "secret", MaintenanceRequest{})
	var he *maintenanceHTTPError
	if !errors.As(err, &he) || he.Status != http.StatusFound || followed {
		t.Fatalf("err=%v followed=%v", err, followed)
	}
}

func TestMaintenanceRejectsEmptySecretBeforeNetwork(t *testing.T) {
	_, err := RequestMaintenance(context.Background(), "://invalid", " \t", MaintenanceRequest{})
	if err == nil || !strings.Contains(err.Error(), "credential is empty") || strings.Contains(err.Error(), "invalid") {
		t.Fatalf("empty secret error = %v", err)
	}
}

func TestRenewMaintenanceOmitsScope(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/maintenance/renew-request" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"request_id":"` + strings.Repeat("a", 32) + `","session_id":"` + strings.Repeat("b", 32) + `","status":"requested","approve_by":"2030-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()
	_, err := RenewMaintenance(context.Background(), srv.URL, "token", MaintenanceRequest{ClientRequestID: strings.Repeat("c", 32), SessionID: strings.Repeat("b", 32), Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["scope"]; ok {
		t.Fatalf("renew body retained scope: %#v", body)
	}
	if body["session_id"] != strings.Repeat("b", 32) {
		t.Fatalf("renew body omitted session ID: %#v", body)
	}
}

func TestMaintenanceRejectsMalformedSuccessReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"request_id":"bad","session_id":"bad","status":"requested","approve_by":"bad","apps":[]}`))
	}))
	defer srv.Close()
	if _, err := RequestMaintenance(context.Background(), srv.URL, "token", MaintenanceRequest{}); err == nil {
		t.Fatal("malformed success reply accepted")
	}
}

func TestMaintenanceCredentialStorageAndValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := strings.Repeat("a", 32)
	c := MaintenanceCredential{SchemaVersion: 1, SessionID: id, BenchID: strings.Repeat("b", 32), Fingerprint: strings.Repeat("c", 64), SessionCredential: strings.Repeat("d", 64)}
	if err := SaveMaintenanceCredential(c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadMaintenanceCredential(id)
	if err != nil || got.SessionCredential != c.SessionCredential {
		t.Fatalf("load = %+v, %v", got, err)
	}
	info, err := os.Stat(maintenanceCredentialPath(id))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("file mode=%v err=%v", info.Mode().Perm(), err)
	}
	dirInfo, err := os.Stat(maintenanceCredentialDir())
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("dir mode=%v err=%v", dirInfo.Mode().Perm(), err)
	}
	if err := os.Chmod(maintenanceCredentialPath(id), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMaintenanceCredential(id); err == nil {
		t.Fatal("insecure credential mode accepted")
	}
	if err := os.Chmod(maintenanceCredentialPath(id), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(maintenanceCredentialDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMaintenanceCredential(id); err == nil {
		t.Fatal("insecure credential directory mode accepted")
	}
	if err := os.Chmod(maintenanceCredentialDir(), 0700); err != nil {
		t.Fatal(err)
	}
	path := maintenanceCredentialPath(id)
	backup := path + ".real"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMaintenanceCredential(id); err == nil {
		t.Fatal("symlink credential accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMaintenanceCredential("../escape"); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if err := DeleteMaintenanceCredential(id); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMaintenanceCredential(id); err == nil {
		t.Fatal("missing credential accepted")
	}
}

func TestRefreshMaintenanceLicenseFailsClosedOffline(t *testing.T) {
	setupSignedCache(t, func(t *testing.T) string {
		fp, err := Fingerprint()
		if err != nil {
			t.Skipf("fingerprint unavailable: %v", err)
		}
		return fp
	})
	if err := SaveLicenseKey("test-key"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, _, err := RefreshMaintenanceLicense(ctx); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh err = %v, want context.Canceled", err)
	}
}
