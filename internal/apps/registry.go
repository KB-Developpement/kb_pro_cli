package apps

// App represents a predefined KB-Developpement Frappe app.
type App struct {
	// Name is the Frappe app name used for bench install-app.
	// Must match the Python package name in setup.py / pyproject.toml.
	Name string
	// URL is the HTTPS git clone URL used for bench get-app.
	URL string
}

// All is the list of KB-Developpement apps available for installation.
var All = []App{
	{Name: "kb_pro", URL: "https://github.com/KB-Developpement/kb_pro"},
	{Name: "kb_compta_v2", URL: "https://github.com/KB-Developpement/kb_compta_v2"},
	{Name: "kb_cheque", URL: "https://github.com/KB-Developpement/kb_cheque"},
	{Name: "HR2025", URL: "https://github.com/KB-Developpement/HR2025"},
	{Name: "kb_facilite", URL: "https://github.com/KB-Developpement/kb_facilite"},
	{Name: "kb_distri", URL: "https://github.com/KB-Developpement/kb_distri"},
	{Name: "kb_print", URL: "https://github.com/KB-Developpement/kb_print"},
	{Name: "kb_commercial", URL: "https://github.com/KB-Developpement/kb_commercial"},
	{Name: "kb_stock", URL: "https://github.com/KB-Developpement/kb_stock"},
	{Name: "AchatsExtern", URL: "https://github.com/KB-Developpement/AchatsExtern"},
}
