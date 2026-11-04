// Package pi carries the Pi worker image's Dockerfile, so the binary can
// build the image without a source checkout (dashboard: Settings, Pi agent).
package pi

import _ "embed"

// Dockerfile is contrib/pi/Dockerfile.
//
//go:embed Dockerfile
var Dockerfile []byte
