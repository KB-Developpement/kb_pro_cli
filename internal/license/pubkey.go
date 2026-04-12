package license

import _ "embed"

// publicKeyPEM is the Ed25519 public key used to verify license JWTs.
// The corresponding private key is held only by the license server.
//
// WARNING: If the private key is compromised, a new key pair must be generated,
// this file updated, and a new CLI version shipped to all clients.
// There is no key rotation mechanism — key security is critical.
//
//go:embed license_signing.pub
var publicKeyPEM []byte
