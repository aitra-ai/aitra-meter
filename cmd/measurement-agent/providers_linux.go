//go:build linux

package main

// NOTE: the nvml provider requires cgo and is omitted from the CGO-free
// deploy/aitra-meter-24 build. See cmd/measurement-agent/main.go for context.
