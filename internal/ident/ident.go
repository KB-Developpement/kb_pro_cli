// Package ident holds the format rules for source-identity values shared by
// the download client and the bench-local state files (contracts section 2.3
// and 6.1). Keeping them in one leaf package means a receipt is validated with
// exactly the rules the response headers were.
package ident

import (
	"regexp"
	"strings"
)

// Org is the GitHub organisation every KB repository lives under.
const Org = "KB-Developpement"

var (
	// RepositoryRE is the X-KB-Repository format.
	RepositoryRE = regexp.MustCompile(`^KB-Developpement/[A-Za-z0-9._-]{1,100}$`)
	// CommitRE is a full lowercase SHA-1 commit id (X-KB-Commit).
	CommitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// SHA256RE is a lowercase hex SHA-256 digest.
	SHA256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

	refRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+-]{0,127}$`)
)

// Repository returns the full "KB-Developpement/<name>" value for a registry
// repository name.
func Repository(name string) string { return Org + "/" + name }

// ValidRef mirrors the license server's validVersion: the characters of a git
// ref, no empty, "." or ".." path segment, at most 128 characters.
func ValidRef(v string) bool {
	if !refRE.MatchString(v) {
		return false
	}
	for _, seg := range strings.Split(v, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
