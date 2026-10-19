package main

import (
	"fmt"
	"io/fs"
)

// Templates holds the read-only inputs used to scaffold a brain folder.
// The caller supplies the assets and the identity-map path used by its runtime.
type Templates struct {
	Prompt          string
	Skills          []TemplateSkill
	IdentityMapPath string
}

// TemplateSkill is one named starter skill, before the brain name is rendered.
type TemplateSkill struct{ Name, Content string }

// TemplatesFrom reads the starter assets in file-name order. Missing embedded
// assets are a programming error, so startup fails with the existing panic.
func TemplatesFrom(source fs.FS, identityMapPath string) Templates {
	read := func(name string) []byte {
		data, err := fs.ReadFile(source, name)
		if err != nil {
			panic(fmt.Errorf("embedded template %s: %w", name, err))
		}
		return data
	}
	prompt := string(read("brain-prompt.example.md"))
	entries, err := fs.ReadDir(source, "skills")
	if err != nil {
		panic(fmt.Errorf("embedded skills: %w", err))
	}
	skills := make([]TemplateSkill, 0, len(entries))
	for _, e := range entries {
		skills = append(skills, TemplateSkill{e.Name(), string(read("skills/" + e.Name()))})
	}
	return Templates{Prompt: prompt, Skills: skills, IdentityMapPath: identityMapPath}
}
