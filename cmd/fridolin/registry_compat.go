package main

import "github.com/Icewater1337/fridolin/internal/registry"

type brainRegistry = registry.Registry
type brainRegistryOptions = registry.Options
type brainAddInput = registry.AddInput
type brainAddResult = registry.AddResult
type brainRegistryView = registry.View

var newBrainRegistry = registry.New
var registryViews = registry.Views
var errReviewedManifestChanged = registry.ErrReviewedManifestChanged
