package main

import (
	"embed"
	"io/fs"
	"sort"
)

// The dashboard and the brain templates are compiled into the binary, so a
// built bridge needs nothing else from the checkout to serve them.
//
//go:embed web
var webFS embed.FS

//go:embed examples/brain-prompt.example.md examples/skills/*.md
var exampleFS embed.FS

func examplePromptTemplate() string {
	data, err := exampleFS.ReadFile("examples/brain-prompt.example.md")
	if err != nil {
		return "# {{BRAIN_NAME}} assistant\n"
	}
	return string(data)
}

type exampleSkill struct{ name, content string }

func exampleSkills() []exampleSkill {
	entries, err := fs.ReadDir(exampleFS, "examples/skills")
	if err != nil {
		return nil
	}
	var out []exampleSkill
	for _, e := range entries {
		data, err := exampleFS.ReadFile("examples/skills/" + e.Name())
		if err == nil {
			out = append(out, exampleSkill{e.Name(), string(data)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}
