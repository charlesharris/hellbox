// Package version holds the hellbox release string.
//
// It is its own package so that a client can report the version without
// importing the daemon, which would link the SQLite driver and everything else
// the daemon needs into a binary that only draws a terminal.
package version

// Version is the hellbox release.
const Version = "0.1.0"
