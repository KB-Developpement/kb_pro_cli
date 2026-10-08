package kbstate

import (
	"fmt"
	"os"
)

// Geteuid returns the effective uid. It is a variable so tests can pretend to
// be a different user than the bench owner.
var Geteuid = os.Geteuid

// CheckBenchOwner refuses a bench mutation when the effective user does not own
// the bench root. Without it a root-run command would leave a root-owned .kb/
// that the bench user can no longer write.
func CheckBenchOwner(root string) error {
	owner, ok := pathOwner(root)
	if !ok {
		return nil // cannot tell (missing root is reported elsewhere; Windows)
	}
	if euid := Geteuid(); euid != owner {
		return fmt.Errorf("the bench at %s is owned by uid %d but kb is running as uid %d — run kb as the bench user (for example: su <bench user>), not as another user or root", root, owner, euid)
	}
	return nil
}
