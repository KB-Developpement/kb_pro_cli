package bench

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultSiteFromCommonConfig(t *testing.T) {
	t.Parallel()

	sitesRoot := filepath.Join(t.TempDir(), "sites")
	if err := os.MkdirAll(sitesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(sitesRoot, "common_site_config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"default_site":"foxmayn.nascodes.dev"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := defaultSiteFromCommonConfig(cfgPath)
	if err != nil {
		t.Fatalf("defaultSiteFromCommonConfig: %v", err)
	}
	if got != "foxmayn.nascodes.dev" {
		t.Fatalf("got %q, want foxmayn.nascodes.dev", got)
	}
}

func TestListFrappeSiteDirs_ignoresNonSiteDirectories(t *testing.T) {
	t.Parallel()

	sitesRoot := filepath.Join(t.TempDir(), "sites")
	if err := os.MkdirAll(filepath.Join(sitesRoot, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sitesRoot, "__pycache__"), 0o755); err != nil {
		t.Fatal(err)
	}

	realSite := filepath.Join(sitesRoot, "foxmayn.nascodes.dev")
	if err := os.MkdirAll(realSite, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realSite, "site_config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := listFrappeSiteDirs(sitesRoot)
	if err != nil {
		t.Fatalf("listFrappeSiteDirs: %v", err)
	}
	if len(got) != 1 || got[0] != "foxmayn.nascodes.dev" {
		t.Fatalf("got %v, want [foxmayn.nascodes.dev]", got)
	}
}

func TestListFrappeSiteDirs_multipleSites(t *testing.T) {
	t.Parallel()

	sitesRoot := filepath.Join(t.TempDir(), "sites")
	for _, name := range []string{"a.example.com", "b.example.com"} {
		dir := filepath.Join(sitesRoot, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "site_config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := listFrappeSiteDirs(sitesRoot)
	if err != nil {
		t.Fatalf("listFrappeSiteDirs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 sites", got)
	}
}
