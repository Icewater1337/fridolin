package main

// groupRules are the parts of a brain's routing that decide what
// addresses the bridge in the brain's groups (see BrainRouting).
type groupRules struct {
	files    bool // a file counts as addressing the bridge
	forwards bool // a forwarded message does
}

// defaultGroupRules are what a group without a brain, or a brain that
// sets neither, goes by: files and forwards address the bridge, as they
// always did.
var defaultGroupRules = groupRules{files: true, forwards: true}
