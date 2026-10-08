package cli

import "github.com/KB-Developpement/kb_pro_cli/internal/license"

// License access is read through these variables so tests can run the run*
// functions without a signed token. Production never reassigns them.
var (
	syncLicenseFn = license.RunSyncCheck
	allowedSetFn  = license.AllowedSet
	cachedTokenFn = license.GetCachedToken
)

// downloadConcurrency bounds parallel archive downloads. Tests set it to 1 to
// prove registration order does not depend on download timing.
var downloadConcurrency = 3
