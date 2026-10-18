package main

import (
	"embed"
	"fmt"
	"io/fs"
)

// The dashboard and the brain templates are compiled into the binary, so a
// built bridge needs nothing else from the checkout to serve them. Only the
// page is embedded: web/ also holds its JavaScript tests and the package.json
// node reads for them.
//
//go:embed web/index.html web/favicon.svg web/assets
var webFS embed.FS

//go:embed examples/brain-prompt.example.md examples/skills/*.md
var exampleFS embed.FS

// The templates are read once, at startup. go:embed fails the build when a
// pattern above matches nothing, so a read can only fail when a name here
// drifted from its pattern: a programming error, which stops the bridge
// rather than scaffold every new brain with less than it should.
var (
	examplePrompt   = string(mustReadExample("examples/brain-prompt.example.md"))
	exampleSkillSet = readExampleSkills()
)

func examplePromptTemplate() string { return examplePrompt }

type exampleSkill struct{ name, content string }

// exampleSkills are the starter skills, in file name order.
func exampleSkills() []exampleSkill { return exampleSkillSet }

func readExampleSkills() []exampleSkill {
	entries, err := fs.ReadDir(exampleFS, "examples/skills")
	if err != nil {
		panic(fmt.Errorf("embedded skills: %w", err))
	}
	// ReadDir sorts by name.
	out := make([]exampleSkill, 0, len(entries))
	for _, e := range entries {
		out = append(out, exampleSkill{e.Name(), string(mustReadExample("examples/skills/" + e.Name()))})
	}
	return out
}

func mustReadExample(name string) []byte {
	data, err := exampleFS.ReadFile(name)
	if err != nil {
		panic(fmt.Errorf("embedded template %s: %w", name, err))
	}
	return data
}
