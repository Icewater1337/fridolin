// Package testutil supplies isolated test startup, fake provider/Plaud CLIs,
// golden comparisons and small waiting helpers. It imports only the standard
// library and may be imported only by tests. Install Main before using its
// fake executable helpers; subprocess dispatch precedes test isolation.
package testutil
