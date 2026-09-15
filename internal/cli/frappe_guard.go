package cli

import (
	"errors"

	"github.com/KB-Developpement/kb_pro_cli/internal/bench"
)

// stockFrappeRefusal is the message shown when a KB app command is run on a
// bench whose apps/frappe is still the stock frappe/frappe repository.
const stockFrappeRefusal = "apps/frappe is still stock Frappe — run: kb init-kb-frappe first (KB apps require the KB Frappe fork)\n" +
	"Pass --skip-frappe-check to run anyway."

// requireKBFrappe refuses to run the bench-mutating app commands (add,
// site-install, install, upgrade) while apps/frappe is still stock Frappe.
// KB apps need the KB Frappe fork: installing them on stock Frappe downloads
// and builds the app into the bench and then fails at bench install-app,
// leaving the app present but not installed.
//
// Only a POSITIVE stock match blocks. A detection error — apps/frappe missing,
// git unusable, or remotes that match neither repo — must not block, so benches
// with unknown remotes stay usable. That is the same rule the interactive menu
// follows (ADR-004), which is why the menu hides these actions only when the
// stock repo is positively detected.
//
// skip is the --skip-frappe-check flag: the operator who knows better.
func requireKBFrappe(skip bool) error {
	if skip {
		return nil
	}
	isStock, err := bench.DetectFrappeOrigin()
	if err != nil || !isStock {
		return nil
	}
	return errors.New(stockFrappeRefusal)
}
