package nodeagent

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// fakeDocker stands in for docker when FAKE_DOCKER_DIR is set: it
// records each call in docker.log; save writes the tag into the file.
func fakeDocker(dir string) int {
	args := os.Args[1:]
	appendFile(filepath.Join(dir, "docker.log"), strings.Join(args, " ")+"\n")
	if b, err := os.ReadFile(filepath.Join(dir, "fail-docker-"+args[0])); err == nil {
		fmt.Fprint(os.Stderr, string(b))
		return 1
	}
	if args[0] == "save" {
		_ = os.WriteFile(args[slices.Index(args, "-o")+1], []byte(args[1]), 0o600)
	}
	return 0
}
