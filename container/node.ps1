# node.ps1 — make this Windows machine a node of a fridolin core, natively
# (docs/NODES.md, "Joining a machine"): one agent per machine, which can
# run FrIDE instances, keep backups and become the core.
#
# The core's !join reply has the line to paste into PowerShell, with this
# script of the core's own version:
#
#   & ([scriptblock]::Create((irm <source>/container/node.ps1))) `
#     -Core fridolin-core.TAILNET.ts.net:8766 -Code CODE
#
# It does what node.sh does on macOS and Linux, in the same order, and
# asks before it changes anything: Tailscale (installed with winget where
# it is missing, then signed in), the core's fridolin, fetched from the
# core with the join code and checked against the SHA-256 the core sends
# with it, the join (fridolin node join --no-service, as you), and then
# one elevated step, one UAC prompt: fridolin.exe into Program Files,
# where only administrators can change it, and the node service, a
# Windows Service that runs it as LocalSystem. It asks for no secret; the
# code works once and grants nothing until the owner approves the node.
param(
    [string]$Core = "",
    [string]$Code = "",
    # How the core shows this node (default: the computer's name).
    [string]$Name = "",
    # Folders to lend, PATH or PATH:rw: -Root "$env:USERPROFILE\Fridolin:rw".
    [string[]]$Root = @(),
    # Allow no commands, or only the programs -Allow names.
    [switch]$NoExec,
    [string[]]$Allow = @(),
    # Lend this machine's disks to the core's backups (-BackupRoot: only these).
    [switch]$Backup,
    [string[]]$BackupRoot = @(),
    # Let the core run agent sessions and a terminal in these folders.
    [switch]$Workstation,
    [string[]]$WorkstationRoot = @(),
    # The node's state folder: %LOCALAPPDATA%\fridolin-node by default,
    # %USERPROFILE%\.fridolin-node inside an app package such as the
    # Claude desktop app (docs/NODES.md, "Inside an app package").
    [string]$StateDir = "",
    # More flags for fridolin node join, as they are: -JoinArgs '--no-notify'.
    [string[]]$JoinArgs = @(),
    [switch]$Yes
)

$ErrorActionPreference = "Stop"
# Invoke-WebRequest crawls while it draws its progress bar.
$ProgressPreference = "SilentlyContinue"
function Die([string]$msg) { throw [System.Exception]::new($msg) }
function Say([string]$msg) { Write-Host ""; Write-Host "==> $msg" -ForegroundColor Cyan }
# Runs a native program for its exit status alone, its output discarded.
# Windows PowerShell makes an error of each line a program writes to a
# redirected stderr, which "Stop" would turn into the end of this script.
function Quiet([string]$exe, [string[]]$a) {
    $ErrorActionPreference = "Continue"
    & $exe @a *> $null
}
# Quote is s as a single-quoted PowerShell string.
function Quote([string]$s) { "'" + $s.Replace("'", "''") + "'" }

# Whether this process runs inside an app package (MSIX), as commands in
# the Claude desktop app do: Windows then keeps what it creates under
# AppData in the package's own copy, which the node service never sees.
# Only a file made to find out shows it.
function InAppPackage {
    $probeName = ".fridolin-probe-" + [Guid]::NewGuid().ToString("N")
    $probe = Join-Path $env:LOCALAPPDATA $probeName
    try { [IO.File]::WriteAllText($probe, "") } catch { return $false }
    try {
        $packages = Join-Path $env:LOCALAPPDATA "Packages"
        $copies = @(Get-ChildItem -LiteralPath $packages -Directory -ErrorAction SilentlyContinue |
            Where-Object { Test-Path -LiteralPath (Join-Path $_.FullName "LocalCache\Local\$probeName") })
        return $copies.Count -gt 0
    } finally {
        Remove-Item -LiteralPath $probe -Force -ErrorAction SilentlyContinue
    }
}

function FindTailscale {
    $c = Get-Command tailscale -ErrorAction SilentlyContinue
    if ($c) { return $c.Source }
    $p = Join-Path $env:ProgramFiles "Tailscale\tailscale.exe"
    if (Test-Path -LiteralPath $p) { return $p }
    return ""
}
function TailscaleUp([string]$ts) {
    Quiet $ts @("status")
    return $LASTEXITCODE -eq 0
}

# This run's downloads, outside AppData, so that an administrator's
# PowerShell finds them too.
$work = Join-Path $env:USERPROFILE (".fridolin-install-" + [Guid]::NewGuid().ToString("N").Substring(0, 8))
$keep = $false
try {
    if (-not $Core) { Die "-Core is missing (the core's !join reply has the whole line)" }
    if ($Core -notmatch '^[A-Za-z0-9._-]+:[0-9]{1,5}$') { Die "-Core $Core is not HOST:PORT" }
    $Code = $Code.Trim().ToLowerInvariant()
    if ($Code -and $Code -notmatch '^fj-[a-z0-9]+$') { Die "-Code $Code is not a join code (fj-...)" }
    $coreHost = $Core.Substring(0, $Core.LastIndexOf(':'))
    $corePort = [int]$Core.Substring($Core.LastIndexOf(':') + 1)

    $cpu = ""
    try { $cpu = [string][System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture } catch { $cpu = "" }
    if (-not $cpu) { $cpu = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE } }
    $arch = switch ($cpu) { "X64" { "amd64" } "AMD64" { "amd64" } "Arm64" { "arm64" } default { "" } }
    if (-not $arch) { Die "the core has fridolin for amd64 and arm64, not $cpu" }

    if (-not $StateDir) {
        if (InAppPackage) {
            $StateDir = Join-Path $env:USERPROFILE ".fridolin-node"
        } else {
            $StateDir = Join-Path $env:LOCALAPPDATA "fridolin-node"
        }
    }
    if (-not $Name) { $Name = $env:COMPUTERNAME.ToLowerInvariant() }
    $join = @("--core", $Core, "--name", $Name, "--no-service", "--state-dir", $StateDir)
    foreach ($r in $Root) { $join += @("--root", $r) }
    if ($NoExec) { $join += "--no-exec" }
    foreach ($a in $Allow) { $join += @("--allow", $a) }
    if ($Backup) { $join += "--backup" }
    foreach ($r in $BackupRoot) { $join += @("--backup-root", $r) }
    if ($Workstation) { $join += "--workstation" }
    foreach ($r in $WorkstationRoot) { $join += @("--workstation-root", $r) }
    $join += $JoinArgs
    if ($join -match '^--box') { Die "--box is for a node in a box: box.ps1 makes one (the core's !join reply has its line)" }

    # The folders it lends that do not exist yet, which it makes.
    $missing = @($Root | ForEach-Object { $_ -creplace ':(rw|ro)$', '' } | Where-Object { -not (Test-Path -LiteralPath $_ -PathType Container) })
    $destDir = Join-Path $env:ProgramFiles "fridolin"
    $dest = Join-Path $destDir "fridolin.exe"
    $ts = FindTailscale
    if (-not $ts) {
        $tsPlan = "install it (winget install --id Tailscale.Tailscale -e; Windows asks for permission), then sign this machine in (a link to open)"
    } elseif (TailscaleUp $ts) {
        $tsPlan = "installed and signed in"
    } else {
        $tsPlan = "sign this machine in to your tailnet (a link to open)"
    }

    Write-Host "This makes $Name a node of the fridolin core at $Core, natively: one agent on"
    Write-Host "this machine, which can run FrIDE instances, keep backups and become the core."
    Write-Host ""
    Write-Host "  1. Tailscale: $tsPlan"
    Write-Host "  2. fetch the core's fridolin for windows/$arch from the core with the join code,"
    Write-Host "     and check its SHA-256"
    Write-Host "  3. join the core: fridolin node join CODE $($join -join ' ')"
    Write-Host "  4. one elevated step (a UAC prompt): fridolin.exe into $dest$(if (Test-Path -LiteralPath $dest) { ' (the one there kept as fridolin.exe.prev)' }),"
    Write-Host "     and the node service, the Windows Service fridolin-node, which runs it as LocalSystem"
    if ($missing.Count -gt 0) { Write-Host "  It makes the folders it lends first: $($missing -join ', ')" }
    Write-Host ""
    Write-Host "The node serves nothing until you approve it on the core, and the code works once."
    if (-not $Yes) {
        $answer = Read-Host "Continue? [y/N]"
        if ($answer -notmatch '^(y|yes)$') { Die "stopped; nothing was changed" }
    }

    foreach ($m in $missing) { New-Item -ItemType Directory -Force -Path $m | Out-Null }
    New-Item -ItemType Directory -Force -Path $work | Out-Null

    Say "Tailscale"
    if (-not $ts) {
        if (-not (Get-Command winget -ErrorAction SilentlyContinue)) {
            Start-Process "https://tailscale.com/download/windows"
            Die "winget is missing: install Tailscale from https://tailscale.com/download/windows, then run the line again"
        }
        & winget install --id Tailscale.Tailscale -e --accept-source-agreements --accept-package-agreements
        if ($LASTEXITCODE -ne 0) { Die "winget could not install Tailscale (exit $LASTEXITCODE): install it from https://tailscale.com/download/windows, then run the line again" }
        $env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')
        $ts = FindTailscale
        if (-not $ts) { Die "tailscale.exe is not found after installing it: open a new PowerShell and run the line again" }
        Start-Sleep -Seconds 3
    }
    if (TailscaleUp $ts) {
        Write-Host "This machine is on the tailnet."
    } else {
        Write-Host "Sign in with the account of the core's tailnet; tailscale up prints the link, and it opens here."
        # tailscale up waits for the sign-in; meanwhile, open its link.
        $upOut = Join-Path $work "tailscale-up.out"
        $upErr = Join-Path $work "tailscale-up.err"
        $p = Start-Process -FilePath $ts -ArgumentList "up" -NoNewWindow -PassThru -RedirectStandardOutput $upOut -RedirectStandardError $upErr
        $null = $p.Handle # so that ExitCode is there once it exits
        $shown = $false
        while (-not $p.HasExited) {
            if (-not $shown) {
                $text = ""
                foreach ($f in @($upOut, $upErr)) {
                    try { $text += [IO.File]::ReadAllText($f) } catch { $text += "" }
                }
                $m = [regex]::Match($text, 'https://\S+')
                if ($m.Success) {
                    $shown = $true
                    Write-Host ""
                    Write-Host "  $($m.Value)"
                    Write-Host ""
                    Start-Process $m.Value
                }
            }
            Start-Sleep -Seconds 1
        }
        if ($p.ExitCode -ne 0) {
            Get-Content -LiteralPath $upOut, $upErr -ErrorAction SilentlyContinue | Write-Host
            Die "tailscale up failed"
        }
        if (-not (TailscaleUp $ts)) { Die "this machine is not on the tailnet yet: tailscale status says why" }
        Write-Host "Signed in."
    }

    Say "Waiting for the core at $Core"
    $answers = $false
    for ($i = 0; $i -lt 60 -and -not $answers; $i++) {
        $c = New-Object System.Net.Sockets.TcpClient
        try { $answers = $c.ConnectAsync($coreHost, $corePort).Wait(5000) } catch { $answers = $false } finally { $c.Close() }
        if (-not $answers) { Start-Sleep -Seconds 2 }
    }
    if (-not $answers) { Die "the core at $Core does not answer. Is this machine on the core's tailnet, signed in with the same account (tailscale status lists fridolin-core)?" }
    Write-Host "The core answers."

    if (-not $Code) {
        $Code = (Read-Host "Send !join to fridolin in your WhatsApp chat and type the code it sends").Trim().ToLowerInvariant()
        if ($Code -notmatch '^fj-[a-z0-9]+$') { Die "$Code is not a join code (fj-...)" }
    }

    Say "Fetching fridolin from the core"
    # Fetch stores the core's answer to url in out; it stops with what the
    # core said when that is not 200.
    function Fetch([string]$url, [string]$out) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $out -TimeoutSec 900
        } catch {
            $why = $_.ErrorDetails.Message
            if (-not $why) { $why = $_.Exception.Message }
            Die "the core answered: $("$why".Trim())"
        }
    }
    $base = "http://$Core/join/$Code/"
    $query = "?os=windows&arch=$arch"
    $sumFile = Join-Path $work "fridolin.sha256"
    Fetch ($base + "fridolin.sha256" + $query) $sumFile
    $parts = @(([IO.File]::ReadAllText($sumFile)).Trim() -split '\s+')
    if ($parts.Count -lt 3 -or $parts[0] -notmatch '^[0-9a-f]{64}$') { Die "the core sent no SHA-256" }
    $wantSum = $parts[0]
    $wantSize = [int64]$parts[1]
    $version = $parts[2]
    $exe = Join-Path $work "fridolin.exe"
    Fetch ($base + "fridolin" + $query) $exe
    $gotSum = (Get-FileHash -Algorithm SHA256 -LiteralPath $exe).Hash.ToLowerInvariant()
    $gotSize = (Get-Item -LiteralPath $exe).Length
    if ($gotSum -ne $wantSum -or $gotSize -ne $wantSize) { Die "the fridolin from the core does not match the SHA-256 it sent; nothing was installed" }
    Write-Host "fridolin $version for windows/$arch, SHA-256 $gotSum"

    Say "Joining the core"
    $ErrorActionPreference = "Continue"
    & $exe node join $Code @join
    $joined = $LASTEXITCODE
    $ErrorActionPreference = "Stop"
    if ($joined -ne 0) { Die "joining failed. A code works once, within 10 minutes: send !join again, then run the line again with the new code" }
    $keep = $true

    Say "Installing fridolin and the node service (Windows asks for permission)"
    $log = Join-Path $work "elevated.log"
    $elevated = @"
`$ErrorActionPreference = 'Stop'
`$log = $(Quote $log)
`$dest = $(Quote $dest)
`$state = $(Quote $StateDir)
try {
    New-Item -ItemType Directory -Force -Path $(Quote $destDir) | Out-Null
    `$svc = Get-Service -Name fridolin-node -ErrorAction SilentlyContinue
    if (`$svc -and `$svc.Status -ne 'Stopped') { Stop-Service -Name fridolin-node -Force }
    if (Test-Path -LiteralPath `$dest) { Copy-Item -LiteralPath `$dest -Destination (`$dest + '.prev') -Force }
    Copy-Item -LiteralPath $(Quote $exe) -Destination `$dest -Force
    `$ErrorActionPreference = 'Continue'
    if (`$svc) {
        & `$dest node uninstall --state-dir `$state *>> `$log
    }
    & `$dest node install --state-dir `$state *>> `$log
    if (`$LASTEXITCODE -ne 0) { exit 2 }
    & `$dest node start --state-dir `$state *>> `$log
    if (`$LASTEXITCODE -ne 0) { exit 3 }
} catch {
    `$_ | Out-File -Append -FilePath `$log
    exit 1
}
exit 0
"@
    $powershell = Join-Path $env:SystemRoot "System32\WindowsPowerShell\v1.0\powershell.exe"
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($elevated))
    $status = -1
    try {
        $p = Start-Process -FilePath $powershell -Verb RunAs -Wait -PassThru -WindowStyle Hidden -ArgumentList @("-NoProfile", "-ExecutionPolicy", "Bypass", "-EncodedCommand", $encoded)
        $status = $p.ExitCode
    } catch {
        $status = -1
    }
    if (Test-Path -LiteralPath $log) { Get-Content -LiteralPath $log | Write-Host }
    $installed = (Test-Path -LiteralPath $dest) -and ((Get-FileHash -Algorithm SHA256 -LiteralPath $dest).Hash.ToLowerInvariant() -eq $wantSum)
    if ($status -ne 0 -or -not $installed) {
        Write-Host ""
        Write-Host "The node joined, but fridolin.exe or its service is not installed (exit $status)." -ForegroundColor Yellow
        Write-Host "Finish in a PowerShell run as administrator:"
        Write-Host ""
        Write-Host "  New-Item -ItemType Directory -Force $(Quote $destDir) | Out-Null"
        Write-Host "  Copy-Item $(Quote $exe) $(Quote $dest) -Force"
        Write-Host "  & $(Quote $dest) node install --state-dir $(Quote $StateDir)"
        Write-Host "  & $(Quote $dest) node start --state-dir $(Quote $StateDir)"
        Write-Host ""
        Write-Host "then remove $work. & $(Quote $dest) doctor --node --state-dir $(Quote $StateDir) says what is missing."
        Die "the elevated step did not finish"
    }
    $keep = $false

    Say "Done"
    & $dest node status --state-dir $StateDir
    Write-Host ""
    Write-Host "The core tells you in WhatsApp that $Name joined; reply !approve there (or approve it on the dashboard's Nodes page)."
    Write-Host "Here: & $(Quote $dest) node status --state-dir $(Quote $StateDir), and doctor --node --state-dir $(Quote $StateDir)."
    Write-Host "To remove the node: node stop and node uninstall, elevated, then its state dir; !revoke on the core shuts it out."
} catch {
    Write-Host ""
    Write-Host "node.ps1: $($_.Exception.Message)" -ForegroundColor Red
} finally {
    if (-not $keep -and (Test-Path -LiteralPath $work)) { Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue }
}
