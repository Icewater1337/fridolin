package main

import (
	"bufio"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/brain"
)

// runLogWindow is how much of the end of the activity log currentRunLog
// returns. Earlier lines are scanned only when needed to verify its run.
const runLogWindow = 64 * 1024

// currentRunLog is the brain's activity log since the run in flight, the
// one with process pid, began: "🔧 Tool: …" and "💬 …" lines, the last 40.
// Its part starts at the heading that names its pid (runHeading) and ends
// at the next run's. A run whose heading is not in the log yet has nothing
// to show: what is there belongs to the runs before it.
func currentRunLog(path string, pid int) []string {
	// A run without its process yet has no heading to look for. The Build
	// page asks while the run starts, so nothing is opened for it.
	if pid <= 0 {
		return []string{}
	}
	f, err := os.Open(path)
	if err != nil {
		return []string{}
	}
	defer f.Close()
	var offset int64
	if st, err := f.Stat(); err == nil && st.Size() > runLogWindow {
		offset = st.Size() - runLogWindow
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return []string{}
		}
	}
	data, _ := io.ReadAll(f)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if offset > 0 {
		lines = lines[1:] // most likely the end of a line
	}
	start, end, headings := -1, len(lines), 0
	for i, l := range lines {
		p, ok := brain.RunHeadingPID(l)
		if !ok {
			continue
		}
		headings++
		switch {
		case p == pid:
			start, end = i+1, len(lines)
		case start >= 0 && end == len(lines):
			end = i
		}
	}
	switch {
	case start >= 0:
	case offset > 0 && headings == 0:
		// The visible tail alone cannot distinguish a long current run from
		// an earlier run whose successor has not written its heading yet.
		// Include the first, discarded line in the search: the window may
		// have split a heading in two.
		before := offset + int64(strings.IndexByte(string(data), '\n')+1)
		if precedingRunPID(io.NewSectionReader(f, 0, before)) != pid {
			return []string{}
		}
		start = 0
	default:
		return []string{}
	}
	out := slices.Clone(lines[start:end])
	if len(out) > 40 {
		out = out[len(out)-40:]
	}
	return out
}

// precedingRunPID reads headings without retaining the log. Oversized lines
// cannot establish ownership; skip all their fragments rather than interpreting
// a fragment as a heading. Ordinary tool output may be arbitrarily long.
func precedingRunPID(r io.Reader) int {
	reader := bufio.NewReaderSize(r, runLogWindow)
	pid := 0
	fragment := false
	for {
		line, err := reader.ReadSlice('\n')
		if !fragment && err != bufio.ErrBufferFull {
			if p, ok := brain.RunHeadingPID(strings.TrimSuffix(string(line), "\n")); ok {
				pid = p
			}
		}
		fragment = err == bufio.ErrBufferFull
		if err != nil && err != bufio.ErrBufferFull {
			if err != io.EOF {
				return 0
			}
			return pid
		}
	}
}
