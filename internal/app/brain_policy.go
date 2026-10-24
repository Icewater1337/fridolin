package app

import "time"

// shutdownGrace is how long the run in flight gets to finish and be
// delivered when the bridge stops: most answers come within it, and a
// service manager loses patience not long after (launchd kills the bridge
// 20 seconds after SIGTERM). The default of waits.shutdownGrace, a
// variable so that the tests' bridges stop without one (TestMain).
var shutdownGrace = 15 * time.Second
