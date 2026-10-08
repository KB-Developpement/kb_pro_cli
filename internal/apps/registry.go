package apps

// App represents a predefined KB-Developpement Frappe app.
type App struct {
	// Name is the license/download identifier (the JWT allowed_apps value) and,
	// in Phase 0, also the Frappe app name used for bench install-app and the
	// GitHub repository name.
	Name string
	// URL is the HTTPS git clone URL used for bench get-app.
	URL string
	// Tier is informational metadata: "standard" or "full".
	// The authoritative allowed_apps list comes from the license JWT, not this field.
	Tier string
	// Line is the release line this build installs (the kb_frappe major the
	// app's releases target). It is explicit, mirrored in the server's
	// app_lines, and never derived from an app's own semver. Zero means "no
	// line": downloads for such a row are refused before any HTTP request.
	Line int
	// Package is the Python/Frappe app name. Phase 0 defaults it to Name.
	Package string
	// Directory is the apps/<directory> folder. Phase 0 defaults it to Name.
	Directory string
}

// row builds a registry row with the Phase 0 defaults: line 1, and package and
// directory equal to the app name.
func row(name, tier string) App {
	return App{
		Name:      name,
		URL:       "https://github.com/KB-Developpement/" + name,
		Tier:      tier,
		Line:      1,
		Package:   name,
		Directory: name,
	}
}

// LicenseID is the identifier used in the license token and in /download/{app}.
func (a App) LicenseID() string { return a.Name }

// Repository is the GitHub repository name under the KB-Developpement org.
// Phase 0: equal to Name.
func (a App) Repository() string { return a.Name }

// PackageName is the Python/Frappe app name, defaulting to Name.
func (a App) PackageName() string {
	if a.Package != "" {
		return a.Package
	}
	return a.Name
}

// Dir is the apps/<directory> folder name, defaulting to Name.
func (a App) Dir() string {
	if a.Directory != "" {
		return a.Directory
	}
	return a.Name
}

// All is the list of KB-Developpement apps available for installation.
var All = []App{
	row("kb_pro", "standard"),
	row("kb_compta", "standard"),
	row("kb_cheque", "standard"),
	row("kb_facilite", "standard"),
	row("kb_print", "standard"),
	row("kb_stock", "standard"),
	row("HR2025", "full"),
	row("kb_distri", "full"),
	row("kb_commercial", "full"),
	row("AchatsExtern", "full"),
}

// Framework is the internal identity of the KB Frappe fork: license id
// kb_frappe, installed as directory and package "frappe", release line 1. It
// is used only for receipts, adopt and init-kb-frappe. It is deliberately NOT
// part of All, so upgrade, --all and every picker behave as before.
var Framework = App{
	Name:      "kb_frappe",
	URL:       "https://github.com/KB-Developpement/kb_frappe",
	Tier:      "standard",
	Line:      1,
	Package:   "frappe",
	Directory: "frappe",
}

// ByName returns the registry row (or the framework identity) for a license id.
func ByName(name string) (App, bool) {
	for _, a := range All {
		if a.Name == name {
			return a, true
		}
	}
	if name == Framework.Name {
		return Framework, true
	}
	return App{}, false
}

// ByDirectory returns the registry row (or the framework identity) installed in
// apps/<dir>.
func ByDirectory(dir string) (App, bool) {
	for _, a := range All {
		if a.Dir() == dir {
			return a, true
		}
	}
	if dir == Framework.Dir() {
		return Framework, true
	}
	return App{}, false
}
