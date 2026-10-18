package main

import (
	"fmt"
	"io/fs"

	"github.com/Icewater1337/fridolin/examples"
)

// The templates are read once, at startup. go:embed fails the build when a
// pattern in examples/embed.go matches nothing, so a read can only fail
// when a name here drifted from its pattern: a programming error, which
// stops the bridge rather than scaffold every new brain with less than it
// should.
var (
	examplePrompt   = string(mustReadExample("brain-prompt.example.md"))
	exampleSkillSet = readExampleSkills()
)

func examplePromptTemplate() string { return examplePrompt }

type exampleSkill struct{ name, content string }

// exampleSkills are the starter skills, in file name order.
func exampleSkills() []exampleSkill { return exampleSkillSet }

func readExampleSkills() []exampleSkill {
	entries, err := fs.ReadDir(examples.FS, "skills")
	if err != nil {
		panic(fmt.Errorf("embedded skills: %w", err))
	}
	// ReadDir sorts by name.
	out := make([]exampleSkill, 0, len(entries))
	for _, e := range entries {
		out = append(out, exampleSkill{e.Name(), string(mustReadExample("skills/" + e.Name()))})
	}
	return out
}

func mustReadExample(name string) []byte {
	data, err := examples.FS.ReadFile(name)
	if err != nil {
		panic(fmt.Errorf("embedded template %s: %w", name, err))
	}
	return data
}
