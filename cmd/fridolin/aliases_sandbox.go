package main

import "github.com/Icewater1337/fridolin/internal/sandbox"

type sandboxPolicy = sandbox.Settings
type sandboxContext = sandbox.Context
type sandboxStatePaths = sandbox.StatePaths
type sandboxProfile = sandbox.Policy
type sandboxInstallLayout = sandbox.InstallLayout
type bridgePrograms = sandbox.Programs
type protectedPath = sandbox.ProtectedPath

var protectedPathIn = sandbox.ProtectedIn
var buildBrainSandboxProfile = sandbox.Profile
var sandboxHomeReadDenies = sandbox.HomeReadDenies
var sandboxUserFileFolders = sandbox.UserFileFolders

const sandboxExecPath = sandbox.ExecPath

var browserProfilePattern = sandbox.BrowserProfilePattern

type siteProfileInput = sandbox.SitePolicy
type siteBridgeLimits = sandbox.SiteLimits
type siteModuleCaps = sandbox.SiteModule
type siteFileCap = sandbox.FileCapability

var resolveModulePath = sandbox.ResolveModulePath
var resolveSiteCommand = sandbox.ResolveSiteCommand
var buildSiteSandboxProfile = sandbox.SiteProfile
