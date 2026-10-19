package app

import (
	"github.com/Icewater1337/fridolin/examples"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
	"github.com/Icewater1337/fridolin/internal/registry"
)

// Read once at startup, as before: incomplete embedded assets must fail before
// the bridge can create a new brain folder with incomplete content.
var brainTemplates = registry.TemplatesFrom(examples.FS, prompttext.IdentityMapPath)
