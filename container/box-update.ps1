# The box's updater on Windows (docs/NODES.md, "Box updates"), as
# box-update.sh is on macOS and Linux: box.ps1 writes it beside the box's
# compose file (fridolin box updater ps1 prints it) and a Scheduled Task
# runs it every 2 minutes while the owner is signed in, hidden.
#
# Each round a throwaway container of the image the box runs now checks
# the core's request (its signature against the core's key the box
# pinned, the images' registry and build provenance: fridolin box
# pending); then it pulls the images by digest, writes .env and the new
# compose files (compose.yaml, and core.yaml with seccomp.json: the core's
# settings), recreates the services, and goes back to the files of
# before when the node is not connected again within 5 minutes.
$ErrorActionPreference = "Continue"
Set-Location -LiteralPath $PSScriptRoot
$volume = "fridolin-node"
function Log([string]$msg) { Add-Content -LiteralPath (Join-Path $PSScriptRoot "update.log") -Value ("{0} {1}" -f (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ"), $msg) }
function EnvValue([string]$name) {
    $line = Get-Content -LiteralPath ".env" | Where-Object { $_ -like "$name=*" } | Select-Object -First 1
    if ($line) { return $line.Substring($name.Length + 1) } else { return "" }
}
function Result([string]$image, [string]$outcome, [string]$msg = "") {
    docker run --rm -v "${volume}:/node" --entrypoint /opt/fridolin/fridolin $image box result --state-dir /node $outcome $msg *>> update.log
}

# One round at a time.
$lock = Join-Path $PSScriptRoot ".update.lock"
try { $fs = [IO.File]::Open($lock, 'OpenOrCreate', 'ReadWrite', 'None') } catch { exit 0 }
try {
    docker info *> $null
    if ($LASTEXITCODE -ne 0) { exit 0 }
    $current = EnvValue "FRIDOLIN_BOX_NODE_IMAGE"
    if (-not $current) { exit 0 }
    # Until docker compose made the node's volume (box.ps1 installs the
    # updater first), there is nothing to update; a docker run -v here would
    # make the volume outside compose, which then never removes it with down -v.
    docker volume inspect $volume *> $null
    if ($LASTEXITCODE -ne 0) { exit 0 }
    $answer = docker run --rm -v "${volume}:/node" --entrypoint /opt/fridolin/fridolin $current box pending --state-dir /node 2>> update.log
    if ($LASTEXITCODE -eq 3) { exit 0 }
    if ($LASTEXITCODE -ne 0) { Log "the core's request was refused (above); nothing changed"; exit 0 }
    $get = { param($k) ($answer | Where-Object { $_ -like "$k=*" } | Select-Object -First 1) -replace "^$k=", "" }
    $version = & $get "VERSION"; $node = & $get "NODE_IMAGE"; $fride = & $get "FRIDE_IMAGE"
    foreach ($v in @($version, $node, $fride)) {
        if (-not $v -or $v -match '[^A-Za-z0-9._/:@+-]') { Log "box pending printed something else than a version and two images"; exit 0 }
    }
    Log "updating to ${version}: $node"
    docker pull -q $node *>> update.log
    if ($LASTEXITCODE -ne 0) { Log "pulling $node failed"; Result $current failed "pulling the node's image failed"; exit 0 }
    if ((EnvValue "COMPOSE_PROFILES") -eq "fride") {
        docker pull -q $fride *>> update.log
        if ($LASTEXITCODE -ne 0) { Log "pulling $fride failed"; Result $current failed "pulling FrIDE's image failed"; exit 0 }
    }
    Copy-Item .env .env.prev -Force
    Copy-Item compose.yaml compose.yaml.prev -Force
    foreach ($f in @("core.yaml", "seccomp.json")) { if (Test-Path -LiteralPath $f) { Copy-Item $f "$f.prev" -Force } }
    $lines = Get-Content -LiteralPath .env.prev | ForEach-Object {
        if ($_ -like "FRIDOLIN_BOX_NODE_IMAGE=*") { "FRIDOLIN_BOX_NODE_IMAGE=$node" }
        elseif ($_ -like "FRIDOLIN_BOX_FRIDE_IMAGE=*") { "FRIDOLIN_BOX_FRIDE_IMAGE=$fride" }
        else { $_ }
    }
    [IO.File]::WriteAllLines((Join-Path $PSScriptRoot ".env"), $lines)
    $compose = docker run --rm --entrypoint /opt/fridolin/fridolin $node box compose
    if ($LASTEXITCODE -eq 0 -and -not (($compose -join "`n") -match 'docker\.sock')) {
        [IO.File]::WriteAllText((Join-Path $PSScriptRoot "compose.yaml"), (($compose -join "`n") + "`n"))
        # The core's settings, from an image that has them.
        $core = docker run --rm --entrypoint /opt/fridolin/fridolin $node box core-compose 2>$null
        if ($LASTEXITCODE -eq 0 -and -not (($core -join "`n") -match 'docker\.sock')) {
            $seccomp = docker run --rm --entrypoint /opt/fridolin/fridolin $node box seccomp 2>$null
            if ($LASTEXITCODE -eq 0) {
                [IO.File]::WriteAllText((Join-Path $PSScriptRoot "core.yaml"), (($core -join "`n") + "`n"))
                [IO.File]::WriteAllText((Join-Path $PSScriptRoot "seccomp.json"), (($seccomp -join "`n") + "`n"))
                if (-not (EnvValue "COMPOSE_FILE")) { Add-Content -LiteralPath ".env" -Value "COMPOSE_FILE=compose.yaml;core.yaml" }
            }
        }
        $script = docker run --rm --entrypoint /opt/fridolin/fridolin $node box updater ps1
        if ($LASTEXITCODE -eq 0) { [IO.File]::WriteAllText((Join-Path $PSScriptRoot "update.ps1"), (($script -join "`r`n") + "`r`n")) }
    }
    $healthy = $false
    docker compose up -d --remove-orphans *>> update.log
    if ($LASTEXITCODE -eq 0) {
        for ($i = 0; $i -lt 30; $i++) {
            Start-Sleep -Seconds 10
            $id = docker compose ps -q node 2>$null
            if ($id -and ((docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' $id 2>$null) -eq "healthy")) { $healthy = $true; break }
        }
    }
    if ($healthy) {
        Log "the box runs $version"
        Result $node ok
        Remove-Item .env.prev, compose.yaml.prev, core.yaml.prev, seccomp.json.prev -Force -ErrorAction SilentlyContinue
    } else {
        Log "the node did not connect to the core within 5 minutes on $version; going back"
        Move-Item .env.prev .env -Force
        Move-Item compose.yaml.prev compose.yaml -Force
        foreach ($f in @("core.yaml", "seccomp.json")) { if (Test-Path -LiteralPath "$f.prev") { Move-Item "$f.prev" $f -Force } }
        docker compose up -d --remove-orphans *>> update.log
        Result $current failed "the node did not connect to the core within 5 minutes on the new images; back on the old ones"
    }
} finally {
    $fs.Close()
}
