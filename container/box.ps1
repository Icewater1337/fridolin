# box.ps1 — make this Windows machine a node of a fridolin core, as a node
# in a box (docs/NODES.md, "Node in a box"): two containers in Docker
# Desktop, from the images CI publishes, with nothing else installed.
#
# The core's !join reply has the line to paste into PowerShell, with this
# script of the core's own version:
#
#   & ([scriptblock]::Create((irm <source>/container/box.ps1))) -Registry ghcr.io/OWNER `
#     -Version VERSION -Core fridolin-core.TAILNET.ts.net:8766 -Code CODE
#
# It does what box.sh does on macOS and Linux, in the same order, and asks
# before it starts. It needs no administrator: Docker Desktop's own
# install is the one step that does. Running it again with a new code
# joins again; what it made stays.
param(
    [Parameter(Mandatory = $true)][string]$Registry,
    [Parameter(Mandatory = $true)][string]$Version,
    [string]$Core = "",
    [string]$Code = "",
    [string]$Name = $env:COMPUTERNAME,
    # A host folder for the backups, such as F:\Fridolin (default: a volume).
    [string]$Backups = "",
    [switch]$NoFride,
    [string]$Dir = (Join-Path $env:LOCALAPPDATA "fridolin-box"),
    [switch]$Yes,
    # Update a box made before to -Version: pull, pin, write the compose
    # file, recreate; the node keeps its key and approval.
    [switch]$Update
)

$ErrorActionPreference = "Stop"
function Die([string]$msg) { Write-Host "box.ps1: $msg" -ForegroundColor Red; exit 1 }
function Say([string]$msg) { Write-Host ""; Write-Host "==> $msg" -ForegroundColor Cyan }
# Runs a native program and stops when it fails.
function Run([string]$exe, [string[]]$a) {
    & $exe @a
    if ($LASTEXITCODE -ne 0) { throw "$exe $($a -join ' ') failed ($LASTEXITCODE)" }
}

if (-not $Update -and -not $Core) { Die "-Core is missing (the core's !join reply has the whole line)" }
if ($Update) {
    $envFile = Join-Path $Dir ".env"
    if (-not (Test-Path -LiteralPath $envFile)) { Die "no box in $Dir to update (-Dir names another folder)" }
    $old = Get-Content -LiteralPath $envFile
    if (-not ($old -match '^COMPOSE_PROFILES=fride')) { $NoFride = [switch]$true }
    if (-not $Backups) {
        $b = $old | Where-Object { $_ -like 'FRIDOLIN_BOX_BACKUPS=*' } | Select-Object -First 1
        if ($b) { $Backups = $b.Substring('FRIDOLIN_BOX_BACKUPS='.Length) }
    }
}
if ("$Registry$Version" -match '[^A-Za-z0-9._/:-]') { Die "-Registry or -Version has characters an image name cannot" }
$Name = $Name.ToLowerInvariant()
if ($Backups) {
    if (-not (Test-Path -LiteralPath $Backups -PathType Container)) { Die "-Backups $Backups is not a folder" }
    $Backups = (Resolve-Path -LiteralPath $Backups).Path
}
$fride = -not $NoFride
$nodeRef = "$Registry/fridolin-core:$Version"
$frideRef = "$Registry/fridolin-fride:$Version"

if ($Update) {
    Write-Host "This updates the box in $Dir to ${Version}: it pulls the images, pins them, writes the compose file and recreates the services."
} else {
Write-Host "This makes $Name a node of the fridolin core at $Core, in a box:"
Write-Host ""
Write-Host "  1. pull $nodeRef$(if ($fride) { " and $frideRef" }), pinned by digest in $Dir\.env"
Write-Host "  2. write the box's compose file there (docker compose project fridolin-box)"
Write-Host "  3. sign the box in to your tailnet as its own device (a link to open)"
Write-Host "  4. join the core with the code from !join, and start the box"
if ($fride) { Write-Host "  5. FrIDE: sign Claude in for its agents, set their git identity, pair it with the node" }
}
Write-Host ""
Write-Host "No service gets the Docker socket; FrIDE never sees the node's key or backups."
if (-not $Yes) {
    $answer = Read-Host "Continue? [y/N]"
    if ($answer -notmatch '^(y|yes)$') { Die "stopped; nothing was changed" }
}

Say "Checking Docker"
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { Die "docker is not installed: winget install --id Docker.DockerDesktop -e, then start Docker Desktop once" }
docker compose version *> $null
if ($LASTEXITCODE -ne 0) { Die "docker compose is missing (it comes with Docker Desktop)" }
docker info *> $null
if ($LASTEXITCODE -ne 0) { Die "Docker does not answer: start Docker Desktop and run this again" }

Say "Pulling the images"
function Pin([string]$ref) {
    docker pull -q $ref | Out-Null
    if ($LASTEXITCODE -ne 0) { Die "pulling $ref failed (is the version on the registry, and the package public?)" }
    $repo = $ref.Substring(0, $ref.LastIndexOf(':'))
    $digest = docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' $ref | Where-Object { $_ -like "$repo@sha256:*" } | Select-Object -First 1
    if (-not $digest) { Die "no digest for $ref" }
    return $digest.Trim()
}
$nodeImage = Pin $nodeRef
Write-Host "node:  $nodeImage"
$frideImage = "none"
if ($fride) {
    $frideImage = Pin $frideRef
    Write-Host "fride: $frideImage"
}
$gh = Get-Command gh -ErrorAction SilentlyContinue
if ($gh) { gh auth status *> $null }
if ($gh -and $LASTEXITCODE -eq 0) {
    $repo = $Registry.Substring("ghcr.io/".Length) + "/fridolin"
    foreach ($img in @($nodeImage) + $(if ($fride) { @($frideImage) } else { @() })) {
        gh attestation verify "oci://$img" --repo $repo --signer-workflow "$repo/.github/workflows/image.yml" | Out-Null
        if ($LASTEXITCODE -ne 0) { Die "$img has no valid build provenance from $repo; not starting it" }
    }
    Write-Host "Build provenance checked with gh."
} else {
    Write-Host "note: gh is not signed in here, so the images' provenance is not checked now; each later update is checked in the box."
}

Say "Writing $Dir"
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
Set-Location -LiteralPath $Dir
$envLines = @("# Written by box.ps1; box updates rewrite the image lines.", "FRIDOLIN_BOX_NODE_IMAGE=$nodeImage", "FRIDOLIN_BOX_FRIDE_IMAGE=$frideImage")
if ($Backups) { $envLines += "FRIDOLIN_BOX_BACKUPS=$Backups" }
$envLines += $(if ($fride) { "COMPOSE_PROFILES=fride" } else { "COMPOSE_PROFILES=" })
# No BOM: compose reads .env as plain UTF-8.
[IO.File]::WriteAllLines((Join-Path $Dir ".env"), $envLines)
$compose = docker run --rm --entrypoint /opt/fridolin/fridolin $nodeImage box compose
if ($LASTEXITCODE -ne 0) { Die "the image did not print its compose file" }
[IO.File]::WriteAllText((Join-Path $Dir "compose.yaml"), (($compose -join "`n") + "`n"))
Write-Host "compose.yaml and .env are in $Dir"

if ($Update) {
    Say "Recreating the box"
    Run docker @("compose", "up", "-d", "--remove-orphans")
    Say "Done: the box runs $Version"
    exit 0
}

Say "Signing the box in to the tailnet"
Write-Host "Open the link it prints and sign in with the account of the core's tailnet (or set `$env:TS_AUTHKEY before running this)."
docker compose run --rm -T -e TS_AUTHKEY node node tailnet --state-dir /node --name $Name
if ($LASTEXITCODE -ne 0) { Die "the box did not get onto the tailnet" }

Say "Joining the core"
if (-not $Code) { $Code = Read-Host "Send !join to fridolin in your WhatsApp chat and type the code it sends" }
$joinArgs = @("compose", "run", "--rm", "-T", "node", "node", "join", $Code, "--core", $Core, "--box", "--name", $Name, "--state-dir", "/node")
if (-not $fride) { $joinArgs += "--box-no-fride" }
& docker @joinArgs
if ($LASTEXITCODE -ne 0) { Die "joining failed. A code works once, within 10 minutes: send !join again, then run this again with -Code NEWCODE" }
Run docker @("compose", "up", "-d")

if ($fride) {
    Say "FrIDE"
    $up = $false
    for ($i = 0; $i -lt 60; $i++) {
        docker compose exec -T fride curl -fsS -o /dev/null http://127.0.0.1:17840/ *> $null
        if ($LASTEXITCODE -eq 0) { $up = $true; break }
        Start-Sleep -Seconds 2
    }
    if (-not $up) { Die "FrIDE did not start: docker compose logs fride" }
    $restart = $false
    docker compose exec -T fride test -s /var/lib/fride/.config/fridolin-box/claude-token
    if ($LASTEXITCODE -ne 0) {
        Write-Host "FrIDE's agents need a Claude sign-in. claude setup-token runs in the box now: open the link it prints, sign in, and it shows a token."
        docker compose exec fride claude setup-token
        $secure = Read-Host "Paste the token (it is not shown)" -AsSecureString
        $token = [Runtime.InteropServices.Marshal]::PtrToStringBSTR([Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure))
        if ($token) {
            $token | docker compose exec -T fride /opt/fride/entrypoint.sh set-claude-token
            $restart = $true
        } else {
            Write-Host "note: no token; later: pipe it into docker compose exec -T fride /opt/fride/entrypoint.sh set-claude-token, then docker compose restart fride"
        }
        $token = $null
    }
    $email = docker compose exec -T fride git config --global user.email 2>$null
    if (-not $email) {
        $gitName = Read-Host "The name FrIDE's agents commit with"
        $gitEmail = Read-Host "Their email"
        if ($gitName) { Run docker @("compose", "exec", "-T", "fride", "git", "config", "--global", "user.name", $gitName) }
        if ($gitEmail) { Run docker @("compose", "exec", "-T", "fride", "git", "config", "--global", "user.email", $gitEmail) }
    }
    docker compose exec -T fride fride pair-companion --json |
        docker compose exec -T node fridolin node fride pair --state-dir /node --url "http://127.0.0.1:17840" --webhook "127.0.0.1:17841" --code -
    if ($LASTEXITCODE -ne 0) { Die "pairing FrIDE with the node failed: docker compose logs node fride" }
    if ($restart) { docker compose restart fride | Out-Null }
}

Say "Done"
docker compose exec -T node fridolin node status --state-dir /node | Select-Object -Skip 1 -First 2
Write-Host ""
Write-Host "The core tells you in WhatsApp that $Name joined; reply !approve <id> there (or approve it on the dashboard's Nodes page)."
Write-Host "In ${Dir}: docker compose ps | logs | stop | start. Docker Desktop starts the box again after a restart once it runs at sign-in (Settings > General)."
Write-Host "Remove the box with docker compose down -v (it deletes the node's key and its backups$(if ($Backups) { " except $Backups" }))."
