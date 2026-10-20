package site

import "fmt"

// Windows has no sessions: a fake started there says so and fails.
func fakeSession() int { fmt.Println("fakeSession: no sessions on Windows"); return 1 }
