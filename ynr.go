// Package ynr holds what the whole binary shares: its version, capabilities and build.
package ynr

// Version is set at build time with -ldflags "-X github.com/eyelock/ynr.Version=<version>".
var Version = "dev"

// Capabilities is the version of what this ynr can do. Tools and the conformance check compare
// against it before relying on an ability (ADR-004).
const Capabilities = "0.1.0"
