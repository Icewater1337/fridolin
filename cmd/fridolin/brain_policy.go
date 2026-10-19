package main

import "time"

// workingNoticeAfter is how long a run goes before the chat is told that
// it is working on it (workingNotice), a variable so tests need not wait
// for it.
var workingNoticeAfter = 10 * time.Second

// postResultGrace is how long a run may go on after its result, a
// variable so tests need not wait for it.
var postResultGrace = 5 * time.Second

// shutdownGrace is how long the run in flight gets to finish and be
// delivered when the bridge stops: most answers come within it, and a
// service manager loses patience not long after (launchd kills the bridge
// 20 seconds after SIGTERM). A variable so tests need not wait for it.
var shutdownGrace = 15 * time.Second
