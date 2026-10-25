package outbound

import (
	"strings"

	"github.com/Icewater1337/fridolin/internal/textx"
)

// CoreForgetReport is what a core host removed of what moves brought it
// (#266): the host secrets by name, the secret files by path, the entries
// of the core's data dir; DataKept says why its data dir stays, Failure
// what could not be removed.
type CoreForgetReport struct {
	Secrets, Files, Data []string
	DataKept, Failure    string
}

// removed lists what r removed, one line each.
func (r CoreForgetReport) removed() []string {
	var lines []string
	add := func(what string, items []string) {
		if len(items) > 0 {
			lines = append(lines, "• "+what+": "+textx.Truncate(strings.Join(items, ", "), 600))
		}
	}
	add("host secrets", r.Secrets)
	add("secret files", r.Files)
	add("the core's data dir", r.Data)
	return lines
}

// dataNote says what became of the data dir when none was removed.
func (r CoreForgetReport) dataNote() string {
	switch {
	case len(r.Data) > 0:
		return ""
	case r.DataKept != "":
		return "\n" + textx.Truncate(r.DataKept, 300) + "."
	case r.Failure == "" && len(r.removed()) > 0:
		return "\nNo move brought it a data dir of the core."
	}
	return ""
}
