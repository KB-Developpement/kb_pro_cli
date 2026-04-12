package apps

// App represents a predefined KB-Developpement Frappe app.
type App struct {
	// Name is the Frappe app name used for bench install-app.
	// Must match the Python package name in setup.py / pyproject.toml.
	Name string
	// URL is the HTTPS git clone URL used for bench get-app.
	URL string
	// Tier is informational metadata: "standard" or "full".
	// The authoritative allowed_apps list comes from the license JWT, not this field.
	Tier string
}

// All is the list of KB-Developpement apps available for installation.
var All = []App{
	{Name: "kb_pro", URL: "https://github.com/KB-Developpement/kb_pro", Tier: "standard"},
	{Name: "kb_compta_v2", URL: "https://github.com/KB-Developpement/kb_compta_v2", Tier: "standard"},
	{Name: "kb_cheque", URL: "https://github.com/KB-Developpement/kb_cheque", Tier: "standard"},
	{Name: "kb_facilite", URL: "https://github.com/KB-Developpement/kb_facilite", Tier: "standard"},
	{Name: "kb_print", URL: "https://github.com/KB-Developpement/kb_print", Tier: "standard"},
	{Name: "kb_stock", URL: "https://github.com/KB-Developpement/kb_stock", Tier: "standard"},
	{Name: "HR2025", URL: "https://github.com/KB-Developpement/HR2025", Tier: "full"},
	{Name: "kb_distri", URL: "https://github.com/KB-Developpement/kb_distri", Tier: "full"},
	{Name: "kb_commercial", URL: "https://github.com/KB-Developpement/kb_commercial", Tier: "full"},
	{Name: "AchatsExtern", URL: "https://github.com/KB-Developpement/AchatsExtern", Tier: "full"},
}
