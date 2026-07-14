// Package version pins the single source of truth for the cowpen version
// string. The CLI, the README badges, and scripts/smoke.sh all assert
// against this value.
package version

// Version is the semantic version of this build.
const Version = "0.1.0"
