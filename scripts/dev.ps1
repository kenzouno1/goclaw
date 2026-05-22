#!/usr/bin/env pwsh
# Start backend (air) + web UI (pnpm dev) concurrently in PowerShell.
# Usage: pwsh ./scripts/dev.ps1   (or)   .\scripts\dev.ps1
# Ctrl-C stops both.

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$airCmd  = Get-Command air  -ErrorAction SilentlyContinue
$pnpmCmd = Get-Command pnpm -ErrorAction SilentlyContinue
if (-not $airCmd)  { Write-Error "air not found. Install: go install github.com/air-verse/air@latest"; exit 1 }
if (-not $pnpmCmd) { Write-Error "pnpm not found. Install: npm i -g pnpm"; exit 1 }

# Load env files into current process env so child processes (air → goclaw) inherit it.
# Later files override earlier ones (.env.local wins over .env).
$loaded = $false
foreach ($name in @('.env', '.env.local')) {
    $envFile = Join-Path $repoRoot $name
    if (-not (Test-Path $envFile)) { continue }
    Write-Host "Loading $envFile ..." -ForegroundColor DarkGray
    foreach ($line in Get-Content $envFile) {
        $trim = $line.Trim()
        if (-not $trim -or $trim.StartsWith('#')) { continue }
        $eq = $trim.IndexOf('=')
        if ($eq -lt 1) { continue }
        $k = $trim.Substring(0, $eq).Trim()
        $v = $trim.Substring($eq + 1).Trim()
        if (($v.StartsWith('"') -and $v.EndsWith('"')) -or ($v.StartsWith("'") -and $v.EndsWith("'"))) {
            $v = $v.Substring(1, $v.Length - 2)
        }
        [Environment]::SetEnvironmentVariable($k, $v, 'Process')
    }
    $loaded = $true
}
if (-not $loaded) {
    Write-Warning "No .env or .env.local at $repoRoot — backend may exit if GOCLAW_POSTGRES_DSN is unset. Run: .\goclaw.exe onboard"
}

# Resolve shim paths (.cmd/.ps1) — Start-Process needs a real executable.
$airPath  = $airCmd.Source
$pnpmPath = $pnpmCmd.Source

Write-Host "Starting backend (air) + web UI (pnpm dev)..." -ForegroundColor Cyan

function Start-Dev($path, $argList, $cwd) {
    $ext = [IO.Path]::GetExtension($path).ToLowerInvariant()
    $sp = @{ WorkingDirectory = $cwd; PassThru = $true; NoNewWindow = $true }
    if ($ext -in '.cmd', '.bat') {
        $sp.FilePath = 'cmd.exe'
        $sp.ArgumentList = @('/c', $path) + $argList
    } elseif ($ext -eq '.ps1') {
        $sp.FilePath = 'pwsh'
        $sp.ArgumentList = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $path) + $argList
    } else {
        $sp.FilePath = $path
        if ($argList.Count -gt 0) { $sp.ArgumentList = $argList }
    }
    return Start-Process @sp
}

$backend = Start-Dev $airPath  @()      $repoRoot
$web     = Start-Dev $pnpmPath @('dev') (Join-Path $repoRoot 'ui/web')

$cleanup = {
    Write-Host "`nStopping dev processes..." -ForegroundColor Yellow
    foreach ($p in @($backend, $web)) {
        if ($p -and -not $p.HasExited) {
            try { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue } catch {}
        }
    }
}

try {
    Register-EngineEvent -SourceIdentifier PowerShell.Exiting -Action $cleanup | Out-Null
    while (-not $backend.HasExited -and -not $web.HasExited) {
        Start-Sleep -Milliseconds 500
    }
} finally {
    & $cleanup
}
