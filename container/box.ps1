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
    [switch]$Update,
    # Do not install the box's updater, which applies the updates the core
    # asks for (docs/NODES.md, "Box updates").
    [switch]$NoUpdater
)

$ErrorActionPreference = "Stop"
function Die([string]$msg) { Write-Host "box.ps1: $msg" -ForegroundColor Red; exit 1 }
function Say([string]$msg) { Write-Host ""; Write-Host "==> $msg" -ForegroundColor Cyan }
# Runs a native program and stops when it fails.
function Run([string]$exe, [string[]]$a) {
    & $exe @a
    if ($LASTEXITCODE -ne 0) { throw "$exe $($a -join ' ') failed ($LASTEXITCODE)" }
}
# Runs a native program for its exit status alone, its output discarded.
# Windows PowerShell makes an error of each line a program writes to a
# redirected stderr, which "Stop" would turn into the end of this script.
function Quiet([string]$exe, [string[]]$a) {
    $ErrorActionPreference = "Continue"
    & $exe @a *> $null
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
Write-Host "  2. write the box's compose files there (docker compose project fridolin-box), with"
Write-Host "     the core's settings, off until a core move hands the core to this box"
Write-Host "  3. sign the box in to your tailnet as its own device (a link to open)"
Write-Host "  4. join the core with the code from !join, and start the box with its updater"
Write-Host "     (every 2 minutes, as you: it applies the updates the core asks for, once"
Write-Host "     their signature and build provenance are checked)"
if ($fride) { Write-Host "  5. FrIDE: sign Claude in for its agents, set their git identity, pair it with the node" }
}
Write-Host ""
Write-Host "No service gets the Docker socket; FrIDE never sees the node's key, its backups or the core's data and secrets."
if (-not $Yes) {
    $answer = Read-Host "Continue? [y/N]"
    if ($answer -notmatch '^(y|yes)$') { Die "stopped; nothing was changed" }
}

Say "Checking Docker"
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { Die "docker is not installed: winget install --id Docker.DockerDesktop -e, then start Docker Desktop once" }
Quiet docker @("compose", "version")
if ($LASTEXITCODE -ne 0) { Die "docker compose is missing (it comes with Docker Desktop)" }
Quiet docker @("info")
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
if ($gh) { Quiet gh @("auth", "status") }
if ($gh -and $LASTEXITCODE -eq 0) {
    $repo = $Registry.Substring("ghcr.io/".Length) + "/fridolin"
    # The registry's name is lowercase and GitHub's need not be, while gh
    # compares --signer-workflow exactly: take GitHub's own spelling.
    $named = & { $ErrorActionPreference = "Continue"; gh api "repos/$repo" --jq .full_name 2>$null }
    if ($LASTEXITCODE -eq 0 -and $named) { $repo = "$named".Trim() }
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
# Settings the owner added to .env (ports, memory, time zone) stay.
$kept = @()
if (Test-Path -LiteralPath (Join-Path $Dir ".env")) {
    $kept = @(Get-Content -LiteralPath (Join-Path $Dir ".env") | Where-Object { $_ -match '^(FRIDOLIN_BOX_(APPARMOR|DASHBOARD_PORT|SITE_PORT|CORE_MEMORY|NODE_MEMORY|FRIDE_MEMORY)|FRIDOLIN_TZ)=' })
}
$envLines = @("# Written by box.ps1; box updates rewrite the image lines.", "FRIDOLIN_BOX_NODE_IMAGE=$nodeImage", "FRIDOLIN_BOX_FRIDE_IMAGE=$frideImage")
if ($Backups) { $envLines += "FRIDOLIN_BOX_BACKUPS=$Backups" }
$envLines += $(if ($fride) { "COMPOSE_PROFILES=fride" } else { "COMPOSE_PROFILES=" })
# The core's settings (core.yaml) beside the box's own; ";" separates them on Windows.
$envLines += "COMPOSE_FILE=compose.yaml;core.yaml"
$envLines += $kept
# No BOM: compose reads .env as plain UTF-8.
[IO.File]::WriteAllLines((Join-Path $Dir ".env"), $envLines)
$compose = docker run --rm --entrypoint /opt/fridolin/fridolin $nodeImage box compose
if ($LASTEXITCODE -ne 0) { Die "the image did not print its compose file" }
[IO.File]::WriteAllText((Join-Path $Dir "compose.yaml"), (($compose -join "`n") + "`n"))
# Not $core: PowerShell's names ignore case, and that one is -Core, which
# the join below passes.
$coreYaml = docker run --rm --entrypoint /opt/fridolin/fridolin $nodeImage box core-compose
if ($LASTEXITCODE -ne 0) { Die "the image did not print the core's settings" }
[IO.File]::WriteAllText((Join-Path $Dir "core.yaml"), (($coreYaml -join "`n") + "`n"))
$seccomp = docker run --rm --entrypoint /opt/fridolin/fridolin $nodeImage box seccomp
if ($LASTEXITCODE -ne 0) { Die "the image did not print its seccomp profile" }
[IO.File]::WriteAllText((Join-Path $Dir "seccomp.json"), (($seccomp -join "`n") + "`n"))
$updater = docker run --rm --entrypoint /opt/fridolin/fridolin $nodeImage box updater ps1
if ($LASTEXITCODE -ne 0) { Die "the image did not print its updater" }
[IO.File]::WriteAllText((Join-Path $Dir "update.ps1"), (($updater -join "`r`n") + "`r`n"))
Write-Host "compose.yaml, core.yaml, seccomp.json, .env and update.ps1 are in $Dir"

# The box's updater (docs/NODES.md, "Box updates"): a Scheduled Task runs
# update.ps1 every 2 minutes while you are signed in, without a window,
# as you. It applies an update the core asked for once a throwaway
# container of the current image checked it, and is the only part that
# reaches Docker.
if ($NoUpdater) {
    Write-Host "note: no updater (-NoUpdater): the box updates only with box.ps1 -Update"
} else {
    $ps = (Get-Command powershell.exe -ErrorAction SilentlyContinue).Source
    if (-not $ps) { $ps = (Get-Process -Id $PID).Path }
    # conhost.exe --headless gives PowerShell, and the docker calls of
    # update.ps1, a console that is never shown: -WindowStyle Hidden alone
    # hides the console only once it is open, so a window flashes every
    # round. Docker Desktop's Windows all have --headless (1809 and later).
    $conhost = Join-Path $env:SystemRoot "System32\conhost.exe"
    $action = New-ScheduledTaskAction -Execute $conhost -Argument ("--headless `"$ps`" -NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File `"" + (Join-Path $Dir "update.ps1") + "`"")
    $trigger = New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Minutes 2)
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Minutes 30)
    Register-ScheduledTask -TaskName "fridolin box updater" -Action $action -Trigger $trigger -Settings $settings -Force | Out-Null
    Write-Host "The updater runs every 2 minutes while you are signed in (Scheduled Task 'fridolin box updater')."
}

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
        Quiet docker @("compose", "exec", "-T", "fride", "curl", "-fsS", "-o", "/dev/null", "http://127.0.0.1:17840/")
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
    $email = & { $ErrorActionPreference = "Continue"; docker compose exec -T fride git config --global user.email 2>$null }
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
Write-Host "Remove the box with docker compose down -v (it deletes the node's key and its backups$(if ($Backups) { " except $Backups" }), and the core's data once this box runs the core: never while it does)."
