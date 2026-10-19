package main

import "github.com/Icewater1337/fridolin/internal/registry"

type BrainIssue = registry.Issue
type BrainPathContext = registry.PathContext

var resolveBrainRefs = registry.Resolve
var checkBrainPath = registry.CheckPath
