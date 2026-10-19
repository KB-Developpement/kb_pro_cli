package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/ansi"

	"github.com/KB-Developpement/kb_pro_cli/internal/license"
)

// driveForm feeds msgs to a huh form the way the tea runtime would: the
// commands of every update run and their messages are fed back. Timers (cursor
// blink) that do not answer at once are dropped.
func driveForm(f *huh.Form, msgs ...tea.Msg) {
	var feed func(tea.Msg)
	run := func(c tea.Cmd) {
		if c == nil {
			return
		}
		ch := make(chan tea.Msg, 1)
		go func() { ch <- c() }()
		select {
		case m := <-ch:
			feed(m)
		case <-time.After(30 * time.Millisecond):
		}
	}
	cmdType := reflect.TypeOf(tea.Cmd(nil))
	feed = func(m tea.Msg) {
		if m == nil {
			return
		}
		// tea.Batch and tea.Sequence answer with a slice of commands.
		if rv := reflect.ValueOf(m); rv.Kind() == reflect.Slice && rv.Type().Elem() == cmdType {
			for i := 0; i < rv.Len(); i++ {
				run(rv.Index(i).Interface().(tea.Cmd))
			}
			return
		}
		_, c := f.Update(m)
		run(c)
	}
	run(f.Init())
	for _, m := range msgs {
		feed(m)
	}
}

func keyEnter() tea.Msg             { return tea.KeyMsg{Type: tea.KeyEnter} }
func keyText(s string) tea.Msg      { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
func keyType(k tea.KeyType) tea.Msg { return tea.KeyMsg{Type: k} }

func updatableRowsFixture() []updateRow {
	return []updateRow{
		{App: "kb_pro", InstalledTag: "v1.0.0", LatestTag: "v1.2.0", State: stateUpdateAvailable},
		{App: "kb_compta", InstalledTag: "v1.2.0", LatestTag: "v1.2.0", State: stateUpToDate},
		{App: "kb_cheque", InstalledTag: "v1.2.0", LatestTag: "v1.2.0", State: stateUpdateAvailable, Note: "tag moved"},
		{App: "kb_stock", State: stateUnknown},
	}
}

func TestUpdateOptions(t *testing.T) {
	opts := updateOptions(updatableRowsFixture())
	if got := optionValues(opts); strings.Join(got, ",") != "kb_pro,kb_cheque" {
		t.Fatalf("values = %v, want only the apps with an update", got)
	}
	if !strings.Contains(opts[0].Key, "v1.0.0 → v1.2.0") || !strings.Contains(opts[1].Key, "tag moved") {
		t.Errorf("labels = %q, %q", opts[0].Key, opts[1].Key)
	}
	if got := updateOptions([]updateRow{{App: "kb_pro", State: stateUpToDate}}); len(got) != 0 {
		t.Errorf("options for a bench without updates = %v", got)
	}
}

func TestUpdatePickFormKeepsThePreselection(t *testing.T) {
	var draft []string
	f := newUpdatePickForm(updateOptions(updatableRowsFixture()), &draft)
	driveForm(f, keyEnter())
	if f.State != huh.StateCompleted {
		t.Fatalf("form state = %v, want completed", f.State)
	}
	if strings.Join(draft, ",") != "kb_pro,kb_cheque" {
		t.Errorf("draft = %v, want both preselected apps", draft)
	}

	draft = nil
	f = newUpdatePickForm(updateOptions(updatableRowsFixture()), &draft)
	driveForm(f, keyText(" "), keyEnter()) // untick the first
	if strings.Join(draft, ",") != "kb_cheque" {
		t.Errorf("draft after unticking = %v", draft)
	}
}

func TestUpgradeAppOptions(t *testing.T) {
	opts := upgradeAppOptions([]upgradeChoice{{App: "kb_pro", Installed: "v1.1.0"}, {App: "kb_stock"}})
	if got := optionValues(opts); strings.Join(got, ",") != "kb_pro,kb_stock" {
		t.Fatalf("values = %v", got)
	}
	if !strings.Contains(opts[0].Key, "installed v1.1.0") || !strings.Contains(opts[1].Key, "no receipt") {
		t.Errorf("labels = %q, %q", opts[0].Key, opts[1].Key)
	}
}

func TestValidateReleaseTag(t *testing.T) {
	for _, ok := range []string{"v1.2.0", "  v1.2.0  ", "release/1", "v1.2.0-rc1"} {
		if err := validateReleaseTag(ok); err != nil {
			t.Errorf("validateReleaseTag(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "   ", "..", "a/../b", "v1 2", "-v1", "v1.2.0;rm", strings.Repeat("a", 129)} {
		if err := validateReleaseTag(bad); err == nil {
			t.Errorf("validateReleaseTag(%q) accepted", bad)
		}
	}
}

func TestUpgradeToFormCompletes(t *testing.T) {
	choices := []upgradeChoice{{App: "kb_pro", Installed: "v1.1.0"}, {App: "kb_compta"}}
	var d upgradeToDraft
	f := newUpgradeToForm(choices, &d)
	driveForm(f, keyType(tea.KeyDown), keyEnter(), keyText("v1.2.0"), keyEnter())
	if f.State != huh.StateCompleted {
		t.Fatalf("form state = %v, want completed", f.State)
	}
	if d.App != "kb_compta" || d.Tag != "v1.2.0" {
		t.Errorf("draft = %+v", d)
	}
}

func TestUpgradeToFormBlocksAnInvalidTag(t *testing.T) {
	var d upgradeToDraft
	f := newUpgradeToForm([]upgradeChoice{{App: "kb_pro"}}, &d)
	driveForm(f, keyEnter(), keyText("a b"), keyEnter())
	if f.State == huh.StateCompleted {
		t.Fatalf("form accepted an invalid tag: %+v", d)
	}
}

func TestEscapeNote(t *testing.T) {
	if got := escapeNote(`kb_pro *x* ` + "`c` \\"); got != `kb\_pro \*x\* `+"\\`c\\` \\\\" {
		t.Errorf("escapeNote = %q", got)
	}
	f := huh.NewForm(huh.NewGroup(huh.NewNote().Title("t").Description(escapeNote("kb_pro_cli")).Next(true)))
	driveForm(f)
	if v := ansi.Strip(f.View()); !strings.Contains(v, "kb_pro_cli") {
		t.Errorf("escaped name is not shown literally:\n%s", v)
	}
}

// ── the two menu items, with the forms and the upgrade replaced ────────────

type upgradeCall struct {
	apps []string
	pins []upgradePin
}

// menuSeams replaces the confirm, the pickers and the upgrade for one test.
func menuSeams(t *testing.T, confirm bool) (*[]upgradeCall, *[]string) {
	t.Helper()
	var calls []upgradeCall
	var confirms []string
	oldConfirm, oldUpgrade, oldPick, oldAsk, oldNote := menuConfirm, runUpgradeMenuFn, pickUpdatesFn, askUpgradeToFn, showNoteFn
	menuConfirm = func(title, description string) (bool, error) {
		confirms = append(confirms, title+"|"+description)
		return confirm, nil
	}
	runUpgradeMenuFn = func(_ context.Context, a []string, p []upgradePin) error {
		calls = append(calls, upgradeCall{a, p})
		return nil
	}
	t.Cleanup(func() {
		menuConfirm, runUpgradeMenuFn, pickUpdatesFn, askUpgradeToFn, showNoteFn = oldConfirm, oldUpgrade, oldPick, oldAsk, oldNote
	})
	return &calls, &confirms
}

func TestMenuCheckUpdatesUpgradesTheSelection(t *testing.T) {
	h := updatesBench(t)
	stubFetchApps(t, serverAnswer(), nil)
	calls, confirms := menuSeams(t, true)
	var offered []updateRow
	pickUpdatesFn = func(rows []updateRow) ([]string, error) { offered = rows; return []string{"kb_pro"}, nil }

	var err error
	out := capture(t, func() { err = runMenuCheckUpdates(context.Background()) })
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0], upgradeCall{apps: []string{"kb_pro"}}) {
		t.Fatalf("upgrade calls = %+v, want one for kb_pro without pins", *calls)
	}
	if len(offered) != 4 {
		t.Errorf("picker got %d rows", len(offered))
	}
	if len(*confirms) != 1 || !strings.Contains((*confirms)[0], "kb_pro: v1.0.0 → v1.2.0") {
		t.Errorf("confirms = %q", *confirms)
	}
	if !strings.Contains(out, "update available") {
		t.Errorf("the table was not shown:\n%s", out)
	}
	_ = h
}

func TestMenuCheckUpdatesDoesNotUpgradeWithoutConsent(t *testing.T) {
	updatesBench(t)
	stubFetchApps(t, serverAnswer(), nil)

	t.Run("confirm declined", func(t *testing.T) {
		calls, _ := menuSeams(t, false)
		pickUpdatesFn = func([]updateRow) ([]string, error) { return []string{"kb_pro"}, nil }
		capture(t, func() { _ = runMenuCheckUpdates(context.Background()) })
		if len(*calls) != 0 {
			t.Errorf("upgrade ran after a declined confirm: %+v", *calls)
		}
	})
	t.Run("nothing picked", func(t *testing.T) {
		calls, confirms := menuSeams(t, true)
		pickUpdatesFn = func([]updateRow) ([]string, error) { return nil, nil }
		capture(t, func() { _ = runMenuCheckUpdates(context.Background()) })
		if len(*calls) != 0 || len(*confirms) != 0 {
			t.Errorf("calls %+v confirms %q after an empty pick", *calls, *confirms)
		}
	})
	t.Run("picker error", func(t *testing.T) {
		calls, _ := menuSeams(t, true)
		boom := errors.New("form failed")
		pickUpdatesFn = func([]updateRow) ([]string, error) { return nil, boom }
		var err error
		capture(t, func() { err = runMenuCheckUpdates(context.Background()) })
		if !errors.Is(err, boom) || len(*calls) != 0 {
			t.Errorf("err = %v, calls = %+v", err, *calls)
		}
	})
}

func TestMenuCheckUpdatesWithNothingToUpdate(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_pro", "v1.2.0", true)
	stubFetchApps(t, &license.AppsResponse{Apps: []license.AppEntry{
		{App: "kb_pro", Lines: []license.AppLine{{Line: 1, Tag: "v1.2.0", Commit: commitFor("kb_pro", "v1.2.0")}}}}}, nil)
	calls, confirms := menuSeams(t, true)
	pickUpdatesFn = func([]updateRow) ([]string, error) {
		t.Error("the picker was built for a bench without updates")
		return nil, nil
	}
	var err error
	out := capture(t, func() { err = runMenuCheckUpdates(context.Background()) })
	if err != nil || len(*calls) != 0 || len(*confirms) != 0 {
		t.Errorf("err %v calls %+v confirms %q", err, *calls, *confirms)
	}
	if !strings.Contains(out, "up to date") {
		t.Errorf("output:\n%s", out)
	}
}

func TestMenuCheckUpdatesFetchError(t *testing.T) {
	h := newBenchH(t)
	h.seed("kb_pro", "v1.0.0", true)
	stubFetchApps(t, nil, license.ErrAppsUnsupported)
	calls, _ := menuSeams(t, true)
	var err error
	capture(t, func() { err = runMenuCheckUpdates(context.Background()) })
	if !errors.Is(err, license.ErrAppsUnsupported) || len(*calls) != 0 {
		t.Errorf("err = %v, calls = %+v", err, *calls)
	}
}

func TestMenuUpgradeToPinsTheTag(t *testing.T) {
	updatesBench(t)
	calls, confirms := menuSeams(t, true)
	var offered []upgradeChoice
	askUpgradeToFn = func(c []upgradeChoice) (string, string, bool, error) {
		offered = c
		return "kb_pro", "v1.2.0", true, nil
	}
	var err error
	capture(t, func() { err = runMenuUpgradeTo(context.Background()) })
	if err != nil {
		t.Fatal(err)
	}
	want := upgradeCall{apps: []string{"kb_pro"}, pins: []upgradePin{{App: "kb_pro", Tag: "v1.2.0"}}}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0], want) {
		t.Fatalf("upgrade calls = %+v, want %+v", *calls, want)
	}
	if len(*confirms) != 1 || !strings.HasPrefix((*confirms)[0], "Upgrade kb_pro to v1.2.0?") {
		t.Errorf("confirms = %q", *confirms)
	}
	var names []string
	for _, c := range offered {
		names = append(names, c.App)
	}
	if strings.Join(names, ",") != "kb_pro,kb_compta,kb_cheque,kb_stock" {
		t.Errorf("offered apps = %v", names)
	}
	if offered[0].Installed != "v1.0.0" || offered[2].Installed != "" {
		t.Errorf("installed tags = %+v", offered)
	}
}

func TestMenuUpgradeToBackAndDecline(t *testing.T) {
	updatesBench(t)
	t.Run("back", func(t *testing.T) {
		calls, confirms := menuSeams(t, true)
		askUpgradeToFn = func([]upgradeChoice) (string, string, bool, error) { return "", "", false, nil }
		capture(t, func() { _ = runMenuUpgradeTo(context.Background()) })
		if len(*calls) != 0 || len(*confirms) != 0 {
			t.Errorf("calls %+v confirms %q after backing out", *calls, *confirms)
		}
	})
	t.Run("declined", func(t *testing.T) {
		calls, _ := menuSeams(t, false)
		askUpgradeToFn = func([]upgradeChoice) (string, string, bool, error) { return "kb_pro", "v1.2.0", true, nil }
		capture(t, func() { _ = runMenuUpgradeTo(context.Background()) })
		if len(*calls) != 0 {
			t.Errorf("upgrade ran after a declined confirm: %+v", *calls)
		}
	})
}

// With no registry app in the bench the item shows a Note, never an empty Select.
func TestMenuUpgradeToWithoutAppsShowsANote(t *testing.T) {
	newBenchH(t)
	calls, _ := menuSeams(t, true)
	askUpgradeToFn = func([]upgradeChoice) (string, string, bool, error) {
		t.Error("the form was built without options")
		return "", "", false, nil
	}
	var shown []string
	showNoteFn = func(title, text string) error { shown = append(shown, title+"|"+text); return nil }
	if err := runMenuUpgradeTo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(shown) != 1 || !strings.Contains(shown[0], "No KB apps") || len(*calls) != 0 {
		t.Errorf("notes = %q, calls = %+v", shown, *calls)
	}
}
