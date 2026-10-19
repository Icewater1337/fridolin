package main

import "github.com/Icewater1337/fridolin/examples"

// Read once at startup, as before: incomplete embedded assets must fail before
// the bridge can create a new brain folder with incomplete content.
var brainTemplates = TemplatesFrom(examples.FS, identityMapFile)
