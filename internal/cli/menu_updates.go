package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/KB-Developpement/kb_pro_cli/internal/ident"
	"github.com/KB-Developpement/kb_pro_cli/internal/ui"
)

// Seams for the update menu items: the forms need a terminal, and the upgrade
// itself changes the bench, so tests replace them.
var (
	pickUpdatesFn    = pickUpdates
	askUpgradeToFn   = askUpgradeTo
	runUpgradeMenuFn = runUpgrade
	showNoteFn       = showNote
)

// noteEscaper escapes the characters huh's Note renders as markdown.
var noteEscaper = strings.NewReplacer("\\", "\\\\", "_", "\\_", "*", "\\*", "`", "\\`")

// escapeNote makes dynamic text safe to show in a Note description: an app
// name like kb_pro would otherwise lose its underscore to italics.
func escapeNote(s string) string { return noteEscaper.Replace(s) }

// showNote shows a read-only message in place of a form that would have no
// options (huh panics on a Select without options). Any key goes back.
func showNote(title, text string) error {
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title(title).
				Description(escapeNote(text)).
				Next(true).
				NextLabel("Back"),
		),
	).WithKeyMap(formKeyMap()).Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return nil
	}
	return err
}

// updateOptions lists the apps with an update available, all preselected. It
// is empty when there is nothing to offer; the caller must not build a Select
// from an empty list.
func updateOptions(rows []updateRow) []huh.Option[string] {
	var opts []huh.Option[string]
	for _, r := range updatableRows(rows) {
		label := fmt.Sprintf("%-16s %s → %s", r.App, r.InstalledTag, r.LatestTag)
		if r.Note != "" {
			label += " (" + r.Note + ")"
		}
		opts = append(opts, huh.NewOption(label, r.App).Selected(true))
	}
	return opts
}

// upgradeSummary describes what the selected apps will move from and to.
func upgradeSummary(rows []updateRow, selected []string) string {
	byApp := map[string]updateRow{}
	for _, r := range rows {
		byApp[r.App] = r
	}
	lines := make([]string, 0, len(selected))
	for _, name := range selected {
		r := byApp[name]
		lines = append(lines, fmt.Sprintf("%s: %s → %s", name, r.InstalledTag, r.LatestTag))
	}
	return strings.Join(lines, "\n")
}

// pickUpdates asks which of the apps with an update to upgrade. It returns
// nil when the user backs out.
func pickUpdates(rows []updateRow) ([]string, error) {
	opts := updateOptions(rows)
	if len(opts) == 0 {
		return nil, nil
	}
	var draft []string
	err := newUpdatePickForm(opts, &draft).Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return append([]string(nil), draft...), nil
}

func newUpdatePickForm(opts []huh.Option[string], draft *[]string) *huh.Form {
	return huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Upgrade which apps?").
				Description("Space to toggle · Enter to confirm · Esc/Ctrl+C to cancel").
				Options(opts...).
				Value(draft),
		),
	).WithKeyMap(formKeyMap())
}

// runMenuCheckUpdates is the Check for updates item: fetch, show the table and,
// for apps with an update, offer to upgrade the ones the user picks.
func runMenuCheckUpdates(ctx context.Context) error {
	rows, err := checkUpdates(ctx, true)
	if err != nil {
		return err
	}
	printUpdateTable(os.Stdout, rows)

	upd := updatableRows(rows)
	if len(upd) == 0 {
		pause()
		return nil
	}
	selected, err := pickUpdatesFn(rows)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		_, _ = fmt.Fprintln(os.Stdout, ui.Dim.Render("Nothing selected."))
		pause()
		return nil
	}
	ok, err := menuConfirm(fmt.Sprintf("Upgrade %d app(s)?", len(selected)), upgradeSummary(rows, selected))
	if err != nil {
		return err
	}
	if !ok {
		_, _ = fmt.Fprintln(os.Stdout, ui.Dim.Render("Cancelled."))
		pause()
		return nil
	}
	return runUpgradeMenuFn(ctx, selected, nil)
}

// upgradeChoice is an installed registry app the user may pin to a release.
type upgradeChoice struct {
	App       string
	Installed string // receipt tag, "" when there is no valid receipt
}

// upgradeChoices are the registry apps present in the bench.
func upgradeChoices() []upgradeChoice {
	var out []upgradeChoice
	for _, in := range collectInstalls() {
		c := upgradeChoice{App: in.App.Name}
		if in.Receipt.Valid() {
			c.Installed = in.Receipt.Receipt.Tag()
		}
		out = append(out, c)
	}
	return out
}

// upgradeAppOptions are the Select rows of the "Upgrade to a release" item.
// The caller must not pass an empty list.
func upgradeAppOptions(choices []upgradeChoice) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(choices))
	for _, c := range choices {
		inst := "no receipt"
		if c.Installed != "" {
			inst = "installed " + c.Installed
		}
		opts = append(opts, huh.NewOption(fmt.Sprintf("%-16s %s", c.App, inst), c.App))
	}
	return opts
}

// validateReleaseTag is the Input rule for the release tag.
func validateReleaseTag(s string) error {
	tag := strings.TrimSpace(s)
	switch {
	case tag == "":
		return errors.New("enter a release tag, for example v1.2.0")
	case !ident.ValidRef(tag):
		return fmt.Errorf("%q is not a valid tag name", tag)
	}
	return nil
}

// upgradeToDraft holds the form's values until the form has completed.
type upgradeToDraft struct {
	App string
	Tag string
}

func newUpgradeToForm(choices []upgradeChoice, d *upgradeToDraft) *huh.Form {
	return huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Upgrade which app?").
				Description("↑/↓ to navigate · Enter to confirm · Esc/Ctrl+C to cancel").
				Options(upgradeAppOptions(choices)...).
				Value(&d.App),
			huh.NewInput().
				Title("Release tag").
				Description("An exact published release, for example v1.2.0. The server refuses drafts, prereleases and branches.").
				Validate(validateReleaseTag).
				Value(&d.Tag),
		),
	).WithKeyMap(formKeyMap())
}

// askUpgradeTo asks for the app and the release tag. ok is false when the user
// backs out. The values are read only after the form completed, and checked
// again because accessible mode skips the field validation.
func askUpgradeTo(choices []upgradeChoice) (app, tag string, ok bool, err error) {
	var d upgradeToDraft
	if err := newUpgradeToForm(choices, &d).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	app, tag = d.App, strings.TrimSpace(d.Tag)
	if err := validateReleaseTag(tag); err != nil {
		return "", "", false, err
	}
	for _, c := range choices {
		if c.App == app {
			return app, tag, true, nil
		}
	}
	return "", "", false, errors.New("no app was selected")
}

// runMenuUpgradeTo is the Upgrade to a release item: one app, one exact
// published tag, then the same strict upgrade as `kb upgrade --to`.
func runMenuUpgradeTo(ctx context.Context) error {
	choices := upgradeChoices()
	if len(choices) == 0 {
		return showNoteFn("Upgrade to a release", "No KB apps from the registry are installed in this bench.")
	}
	app, tag, ok, err := askUpgradeToFn(choices)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	ok, err = menuConfirm(fmt.Sprintf("Upgrade %s to %s?", app, tag),
		"Downloads that published release from the license server, replaces the app and runs bench migrate. A draft, prerelease or branch is refused.")
	if err != nil {
		return err
	}
	if !ok {
		_, _ = fmt.Fprintln(os.Stdout, ui.Dim.Render("Cancelled."))
		pause()
		return nil
	}
	return runUpgradeMenuFn(ctx, []string{app}, []upgradePin{{App: app, Tag: tag}})
}
