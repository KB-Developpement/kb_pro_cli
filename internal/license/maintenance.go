package license

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/KB-Developpement/kb_pro_cli/internal/config"
	"github.com/KB-Developpement/kb_pro_cli/internal/fsutil"
	"github.com/KB-Developpement/kb_pro_cli/internal/version"
)

const maintenanceCredentialHeader = "X-KB-Maintenance-Session"
const maintenanceBodyLimit = 64 << 10

var maintenanceIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
var maintenanceErrorKeyRE = regexp.MustCompile(`^[a-z0-9_]{1,80}$`)
var errMaintenanceCredentialMissing = errors.New("maintenance credential not found")

type MaintenanceRequest struct {
	ClientRequestID  string   `json:"client_request_id"`
	BenchID          string   `json:"bench_id"`
	Fingerprint      string   `json:"fingerprint"`
	LicenseKeySHA256 string   `json:"license_key_sha256"`
	Apps             []string `json:"apps"`
	Scope            string   `json:"scope,omitempty"`
	Reason           string   `json:"reason"`
	SessionID        string   `json:"session_id,omitempty"`
}
type RequestReply struct {
	RequestID string   `json:"request_id"`
	SessionID string   `json:"session_id"`
	Status    string   `json:"status"`
	ApproveBy string   `json:"approve_by"`
	Apps      []string `json:"apps"`
}
type RedeemRequest struct {
	RequestID   string `json:"request_id"`
	EntryCode   string `json:"entry_code"`
	BenchID     string `json:"bench_id"`
	Fingerprint string `json:"fingerprint"`
}
type RedeemReply struct {
	SessionID         string   `json:"session_id"`
	SessionCredential string   `json:"session_credential"`
	Apps              []string `json:"apps"`
	WorkUntil         string   `json:"work_until"`
}
type ReportApp struct {
	App             string   `json:"app"`
	Directory       string   `json:"directory"`
	Repository      string   `json:"repository"`
	Line            string   `json:"line"`
	Branch          string   `json:"branch"`
	Head            string   `json:"head"`
	Base            string   `json:"base"`
	Protected       []string `json:"protected"`
	Dirty           bool     `json:"dirty"`
	UntrackedFiles  int      `json:"untracked_files"`
	Stashes         int      `json:"stashes"`
	LinkedWorktrees int      `json:"linked_worktrees"`
}
type MaintenanceReport struct {
	Seq           int64       `json:"seq"`
	PayloadSHA256 string      `json:"payload_sha256"`
	ObservedAt    string      `json:"observed_at"`
	CLIVersion    string      `json:"cli_version"`
	SourceState   string      `json:"source_state"`
	SchemaChanged bool        `json:"schema_changed"`
	Apps          []ReportApp `json:"apps"`
	Recovery      []string    `json:"recovery"`
}
type ReportReply struct {
	AckSeq        int64  `json:"ack_seq"`
	ApprovalState string `json:"approval_state"`
	WorkUntil     string `json:"work_until"`
}
type MaintenanceClose struct {
	Seq           int64  `json:"seq"`
	PayloadSHA256 string `json:"payload_sha256"`
	ObservedAt    string `json:"observed_at"`
	SourceState   string `json:"source_state"`
}
type CloseReply struct {
	Status   string `json:"status"`
	ClosedAt string `json:"closed_at"`
}
type MaintenanceCredential struct {
	SchemaVersion     int    `json:"schema_version"`
	SessionID         string `json:"session_id"`
	BenchID           string `json:"bench_id"`
	Fingerprint       string `json:"fingerprint"`
	SessionCredential string `json:"session_credential"`
	SavedAt           string `json:"saved_at"`
}

type maintenanceAPIError struct {
	Error string `json:"error"`
}
type maintenanceHTTPError struct {
	Status int
	Key    string
}

func (e *maintenanceHTTPError) Error() string {
	if e.Key != "" {
		return fmt.Sprintf("maintenance server returned %s (HTTP %d)", e.Key, e.Status)
	}
	return fmt.Sprintf("maintenance server returned HTTP %d", e.Status)
}

func maintenanceClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second}).DialContext
	tr.ResponseHeaderTimeout = ResponseHeaderTimeout
	tr.TLSHandshakeTimeout = DialTimeout
	tr.DisableKeepAlives = true
	return &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func maintenancePost(ctx context.Context, serverURL, route, secret string, bearer bool, body, result any) error {
	if strings.TrimSpace(secret) == "" {
		return errors.New("maintenance credential is empty")
	}
	if serverURL == "" {
		serverURL = resolveServerURL()
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode maintenance request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+route, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("build maintenance request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(CLIVersionHeader, version.Version)
	if bearer {
		req.Header.Set("Authorization", "Bearer "+secret)
	} else {
		req.Header.Set(maintenanceCredentialHeader, secret)
	}
	c := maintenanceClient()
	defer c.CloseIdleConnections()
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("maintenance request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maintenanceBodyLimit+1))
	if err != nil {
		return fmt.Errorf("read maintenance response: %w", err)
	}
	if len(data) > maintenanceBodyLimit {
		return errors.New("maintenance response exceeds size limit")
	}
	if resp.StatusCode != http.StatusOK {
		var api maintenanceAPIError
		_ = json.Unmarshal(data, &api)
		if !maintenanceErrorKeyRE.MatchString(api.Error) {
			api.Error = ""
		}
		return &maintenanceHTTPError{Status: resp.StatusCode, Key: api.Error}
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("decode maintenance response: %w", err)
	}
	return nil
}

func RequestMaintenance(ctx context.Context, serverURL, token string, request MaintenanceRequest) (*RequestReply, error) {
	var out RequestReply
	if err := maintenancePost(ctx, serverURL, "/maintenance/request", token, true, request, &out); err != nil {
		return nil, err
	}
	if !maintenanceIDRE.MatchString(out.RequestID) || !maintenanceIDRE.MatchString(out.SessionID) || out.Status != "requested" || !validMaintenanceTime(out.ApproveBy) || out.Apps == nil {
		return nil, errors.New("maintenance server returned invalid request reply")
	}
	return &out, nil
}
func RenewMaintenance(ctx context.Context, serverURL, token string, request MaintenanceRequest) (*RequestReply, error) {
	var out RequestReply
	request.Scope = ""
	if err := maintenancePost(ctx, serverURL, "/maintenance/renew-request", token, true, request, &out); err != nil {
		return nil, err
	}
	if !maintenanceIDRE.MatchString(out.RequestID) || !maintenanceIDRE.MatchString(out.SessionID) || out.Status != "requested" || !validMaintenanceTime(out.ApproveBy) {
		return nil, errors.New("maintenance server returned invalid renew reply")
	}
	return &out, nil
}
func RedeemMaintenance(ctx context.Context, serverURL, token string, request RedeemRequest) (*RedeemReply, error) {
	var out RedeemReply
	if err := maintenancePost(ctx, serverURL, "/maintenance/redeem", token, true, request, &out); err != nil {
		return nil, err
	}
	if !maintenanceIDRE.MatchString(out.SessionID) || !isHex(out.SessionCredential, 64) || !validMaintenanceTime(out.WorkUntil) || out.Apps == nil {
		return nil, errors.New("maintenance server returned invalid redeem reply")
	}
	return &out, nil
}
func ReportMaintenance(ctx context.Context, serverURL, credential string, report MaintenanceReport) (*ReportReply, error) {
	var out ReportReply
	if err := maintenancePost(ctx, serverURL, "/maintenance/report", credential, false, report, &out); err != nil {
		return nil, err
	}
	if out.AckSeq != report.Seq || out.ApprovalState == "" || (out.WorkUntil != "" && !validMaintenanceTime(out.WorkUntil)) {
		return nil, errors.New("maintenance server returned invalid report reply")
	}
	return &out, nil
}
func CloseMaintenance(ctx context.Context, serverURL, credential string, close MaintenanceClose) (*CloseReply, error) {
	var out CloseReply
	if err := maintenancePost(ctx, serverURL, "/maintenance/close", credential, false, close, &out); err != nil {
		return nil, err
	}
	if out.Status != "closed" || !validMaintenanceTime(out.ClosedAt) {
		return nil, errors.New("maintenance server returned invalid close reply")
	}
	return &out, nil
}

func validMaintenanceTime(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func maintenanceCredentialPath(id string) string {
	return filepath.Join(config.ConfigDir(), "maintenance", id+".json")
}
func maintenanceCredentialDir() string { return filepath.Join(config.ConfigDir(), "maintenance") }
func validateMaintenanceCredentialDir() error {
	dirInfo, err := os.Lstat(maintenanceCredentialDir())
	if err != nil {
		if os.IsNotExist(err) {
			return errMaintenanceCredentialMissing
		}
		return fmt.Errorf("inspect maintenance credential directory: %w", err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 || dirInfo.Mode().Perm() != 0700 {
		return errors.New("maintenance credential directory permissions are invalid")
	}
	return nil
}
func SaveMaintenanceCredential(c MaintenanceCredential) error {
	if c.SchemaVersion != 1 || !maintenanceIDRE.MatchString(c.SessionID) || !maintenanceIDRE.MatchString(c.BenchID) || !isHex(c.Fingerprint, 64) || !isHex(c.SessionCredential, 64) {
		return errors.New("invalid maintenance credential")
	}
	if c.SavedAt == "" {
		c.SavedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if !validMaintenanceTime(c.SavedAt) {
		return errors.New("invalid maintenance credential saved_at")
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode maintenance credential: %w", err)
	}
	dir := filepath.Dir(maintenanceCredentialPath(c.SessionID))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create maintenance credential directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("maintenance credential directory is not a real directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("secure maintenance credential directory: %w", err)
	}
	if err := fsutil.WriteFileDurable(maintenanceCredentialPath(c.SessionID), b, 0600); err != nil {
		return fmt.Errorf("write maintenance credential: %w", err)
	}
	return nil
}
func LoadMaintenanceCredential(id string) (*MaintenanceCredential, error) {
	if !maintenanceIDRE.MatchString(id) {
		return nil, errors.New("invalid maintenance session ID")
	}
	if err := validateMaintenanceCredentialDir(); err != nil {
		return nil, err
	}
	path := maintenanceCredentialPath(id)
	fileInfo, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("maintenance credential not found")
		}
		return nil, fmt.Errorf("inspect maintenance credential: %w", err)
	}
	if !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 || fileInfo.Mode().Perm() != 0600 {
		return nil, errors.New("maintenance credential file permissions are invalid")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("maintenance credential not found")
		}
		return nil, fmt.Errorf("read maintenance credential: %w", err)
	}
	var c MaintenanceCredential
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, errors.New("maintenance credential is corrupt")
	}
	if c.SessionID != id || c.SchemaVersion != 1 || !maintenanceIDRE.MatchString(c.BenchID) || !isHex(c.Fingerprint, 64) || !isHex(c.SessionCredential, 64) || !validMaintenanceTime(c.SavedAt) {
		return nil, errors.New("maintenance credential is invalid")
	}
	return &c, nil
}
func DeleteMaintenanceCredential(id string) error {
	if !maintenanceIDRE.MatchString(id) {
		return errors.New("invalid maintenance session ID")
	}
	if err := validateMaintenanceCredentialDir(); err != nil {
		if errors.Is(err, errMaintenanceCredentialMissing) {
			return nil
		}
		return err
	}
	info, err := os.Lstat(maintenanceCredentialPath(id))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect maintenance credential: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0600 {
		return errors.New("maintenance credential file permissions are invalid")
	}
	err = os.Remove(maintenanceCredentialPath(id))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete maintenance credential: %w", err)
	}
	return fsutil.SyncDir(maintenanceCredentialDir())
}
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// RefreshMaintenanceLicense requires an online heartbeat. It returns no cached fallback.
func RefreshMaintenanceLicense(ctx context.Context) (token, fingerprint, keyHash string, allowed map[string]bool, err error) {
	e, err := loadCache()
	if err != nil || e == nil || e.Token == "" {
		if err == nil {
			err = errors.New("no cached license token found — run: kb activate")
		}
		return "", "", "", nil, err
	}
	c, err := verifyToken(e.Token)
	if err != nil {
		return "", "", "", nil, err
	}
	fp, err := Fingerprint()
	if err != nil {
		return "", "", "", nil, fmt.Errorf("compute machine fingerprint: %w", err)
	}
	if c.Fingerprint != fp {
		return "", "", "", nil, errors.New("machine fingerprint changed — run: kb activate")
	}
	key := LoadLicenseKey()
	if key == "" {
		return "", "", "", nil, errors.New("no stored license key found — run: kb activate")
	}
	ks := sha256.Sum256([]byte(key))
	keyHash = hex.EncodeToString(ks[:])
	r := Heartbeat(ctx, "", e.Token, fp)
	if r.Err != nil {
		return "", "", "", nil, r.Err
	}
	nc, err := verifyToken(r.Token)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("verify refreshed license: %w", err)
	}
	if nc.Fingerprint != fp || nc.ClientID != c.ClientID {
		return "", "", "", nil, errors.New("refreshed license fingerprint mismatch")
	}
	if err := saveCache(&cacheEntry{Token: r.Token, ActivatedAt: e.ActivatedAt, LastCheck: time.Now().UTC()}); err != nil {
		return "", "", "", nil, err
	}
	allowed = make(map[string]bool, len(nc.AllowedApps))
	for _, app := range nc.AllowedApps {
		allowed[app] = true
	}
	return r.Token, fp, keyHash, allowed, nil
}
