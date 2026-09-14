package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

const sampleChecksums = `2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae  kb_0.2.0_darwin_arm64.tar.gz
fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9  kb_0.2.0_linux_amd64.tar.gz
`

func TestChecksumFor(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		asset   string
		want    string
		wantErr bool
	}{
		{name: "found", body: sampleChecksums, asset: "kb_0.2.0_linux_amd64.tar.gz", want: "fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9"},
		{name: "found first line", body: sampleChecksums, asset: "kb_0.2.0_darwin_arm64.tar.gz", want: "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"},
		{name: "star prefix", body: "fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9 *kb.tar.gz\n", asset: "kb.tar.gz", want: "fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9"},
		{name: "missing asset", body: sampleChecksums, asset: "kb_0.2.0_linux_arm64.tar.gz", wantErr: true},
		{name: "no substring match", body: "abc  other_kb_0.2.0_linux_amd64.tar.gz.sig\n", asset: "kb_0.2.0_linux_amd64.tar.gz", wantErr: true},
		{name: "empty body", body: "", asset: "kb.tar.gz", wantErr: true},
		{name: "malformed line", body: "nothexdigits  kb.tar.gz\n", asset: "kb.tar.gz", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checksumFor([]byte(tc.body), tc.asset)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checksumFor err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("checksumFor = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("archive bytes")
	sum := sha256.Sum256(data)
	body := hex.EncodeToString(sum[:]) + "  kb.tar.gz\n"

	if err := verifyChecksum(data, []byte(body), "kb.tar.gz"); err != nil {
		t.Fatalf("verifyChecksum on matching data: %v", err)
	}

	err := verifyChecksum([]byte("tampered"), []byte(body), "kb.tar.gz")
	if err == nil {
		t.Fatal("expected a mismatch error for tampered data")
	}
	if !strings.Contains(err.Error(), hex.EncodeToString(sum[:])) {
		t.Errorf("mismatch error should state the expected checksum, got: %v", err)
	}

	if err := verifyChecksum(data, []byte(""), "kb.tar.gz"); err == nil {
		t.Error("expected an error when checksums.txt has no entry for the asset")
	}
}
