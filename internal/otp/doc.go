// Package otp recognizes requested one-time codes and writes their expiring
// handoff files. It does not receive transport messages, route them, run models
// or append caught codes to history. Callers supply roots, paths and chat IDs.
package otp
