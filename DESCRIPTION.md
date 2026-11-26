Fixes FrIDE threads stuck open after their task is gone.

- fride_close treats FrIDE not-found as "already gone" and closes the thread (logged).
- node_update and fride_stop first ask FrIDE whether each blocking thread's task exists and close those that do not.
- node_update and fride_stop take an optional force (only when the owner asked); docs and changelog updated.
- Test: TestFrideStaleThreads.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
