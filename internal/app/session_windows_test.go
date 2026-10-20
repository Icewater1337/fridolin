package app

import "fmt"

// Windows has no sessions or /dev/tty: the tests that start these fakes
// are Unix-only, and a fake started there anyway says so and fails.
func fakeSession() int         { fmt.Println("fakeSession: no sessions on Windows"); return 1 }
func fakeTTY() int             { fmt.Println("fakeTTY: no /dev/tty on Windows"); return 1 }
func fakeTTYParent(string) int { fmt.Println("fakeTTYParent: no /dev/tty on Windows"); return 1 }
