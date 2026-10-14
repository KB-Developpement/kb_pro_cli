package cli

import (
	"crypto/sha256"
	"fmt"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
	"github.com/KB-Developpement/kb_pro_cli/internal/kbstate"
	"github.com/KB-Developpement/kb_pro_cli/internal/license"
	"github.com/spf13/cobra"
)

func ordinaryMaintenanceGate() error { return kbstate.MaintenanceGate(bench.Root()) }

// Gate before startup checks: configuration and cached identity must not change
// as a side effect of a refused command.
func maintenanceCommandGate(cmd *cobra.Command) error {
	for p := cmd; p != nil; p = p.Parent() {
		if p.Name() == "maintenance" {
			return nil
		}
	}
	switch cmd.Name() {
	case "kb", "status", "license", "activate", "completion", "help":
		return nil
	case "update":
		if check, _ := cmd.Flags().GetBool("check"); check {
			return nil
		}
	case "adopt":
		if check, _ := cmd.Flags().GetBool("check"); check {
			return nil
		}
	}
	return ordinaryMaintenanceGate()
}

func maintenanceActivationGate(key string) error {
	s, err := kbstate.ReadMaintenance(bench.Root())
	if err != nil {
		return err
	}
	if s == nil {
		return ordinaryMaintenanceGate()
	}
	if s.SourceState == kbstate.StateArchive && s.Pending == nil {
		return nil
	}
	fp, err := license.Fingerprint()
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	if fp != s.Fingerprint || digest != s.LicenseKeySHA256 {
		return fmt.Errorf("maintenance protects this bench: activation must use the session fingerprint and original license key")
	}
	return nil
}
