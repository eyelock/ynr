// Package names holds the constants for ynr's own telemetry names, generated from its registry
// (telemetry/registry, ADR-007). Code uses these and never a string literal.
package names

//go:generate go run ../registry/gen -registry ../../telemetry/registry -out names.go
