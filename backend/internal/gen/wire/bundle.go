// Package wire contains generated transport models and their authoritative schemas.
package wire

import _ "embed"

//go:embed contracts.gen.json
var Schemas []byte

// SystemEvents is the generated, platform-built catalog of reserved sys.*
// events. It is immutable across releases and independent of any project catalog.
//
//go:embed system-events.gen.json
var SystemEvents []byte
