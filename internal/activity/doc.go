// Package activity owns the dashboard's in-memory feed of recent history
// lines. It must not access runtime brains, transports, configuration, or
// disk directly; the caller supplies history entries and the seed reader.
package activity
