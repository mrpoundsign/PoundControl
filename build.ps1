# PoundDeck Build Script (PowerShell)
# Usage:
#   .\build.ps1                # Run lint, tests, and build Desktop Studio GUI
#   .\build.ps1 -Target gui    # Build Desktop Studio GUI (with Zig CGo) -> build\bin\pounddeck.exe
#   .\build.ps1 -Target test   # Run all unit & headless UI tests
#   .\build.ps1 -Target lint   # Run go fix -diff & golangci-lint-v2
#   .\build.ps1 -Target check  # Run goreleaser check
#   .\build.ps1 -Target clean  # Clean build artifacts

param(
    [ValidateSet("all", "gui", "test", "lint", "check", "clean")]
    [string]$Target = "all"
)

$ErrorActionPreference = "Stop"

function Find-Tool($name, $fallbackPaths) {
    $cmd = Get-Command $name -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    foreach ($p in $fallbackPaths) {
        if ($p -and (Test-Path $p)) { return $p }
    }
    return $null
}

# Resolve Go
$GoExe = Find-Tool "go" @("C:\Program Files\Go\bin\go.exe")
if (-not $GoExe) {
    Write-Error "Go executable not found. Please install Go or ensure it is in PATH."
}

# Refresh PATH with User & Machine environment variables
$userPath = [System.Environment]::GetEnvironmentVariable("Path", "User")
$sysPath = [System.Environment]::GetEnvironmentVariable("Path", "Machine")
$env:Path = "$userPath;$sysPath;$env:Path"

$BuildDir = Join-Path $PSScriptRoot "build"
$BinDir = Join-Path $BuildDir "bin"
if (-not (Test-Path $BuildDir)) { New-Item -ItemType Directory -Path $BuildDir | Out-Null }
if (-not (Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir | Out-Null }

# Resolve Zig (for CGo GUI builds)
$wingetZig = Get-ChildItem "$env:LOCALAPPDATA\Microsoft\WinGet\Packages\zig*" -Recurse -Filter "zig.exe" -ErrorAction SilentlyContinue |
    Select-Object -First 1 -ExpandProperty FullName
$ZigExe = Find-Tool "zig" @(
    $wingetZig,
    "C:\Program Files\zig\zig.exe"
)

function New-WinResSyso {
    param(
        [string]$OutPrefix,
        [string]$ProductName,
        [string]$Description,
        [string]$OriginalFilename
    )
    $iconPath = Join-Path $PSScriptRoot "assets\icon.png"
    if (-not (Test-Path $iconPath)) { return }

    $ver = "0.1.0"
    if ($env:VERSION) {
        $ver = $env:VERSION.TrimStart('v')
    }

    & $GoExe tool go-winres simply `
        --arch amd64 `
        --out $OutPrefix `
        --icon $iconPath `
        --manifest gui `
        --product-name $ProductName `
        --file-description $Description `
        --product-version $ver `
        --file-version $ver `
        --original-filename $OriginalFilename
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Failed to generate Windows resource (.syso) for $OriginalFilename."
    }
}

function Enable-CGoToolchain {
    if (-not $ZigExe) {
        Write-Warning "Zig compiler not found. Attempting build with default system C compiler..."
        $env:CGO_ENABLED = "1"
        return
    }
    Write-Host "Using Zig C compiler: $ZigExe" -ForegroundColor DarkGray
    $env:CGO_ENABLED = "1"
    $env:CC = "zig cc"
}

function Build-GUI {
    Write-Host "`n==> Building PoundDeck Desktop Studio GUI (with CGo)..." -ForegroundColor Cyan
    Enable-CGoToolchain

    if (-not (Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir | Out-Null }
    $outPath = Join-Path $BinDir "pounddeck.exe"
    $sysoPrefix = Join-Path $PSScriptRoot "cmd\pounddeck\rsrc"
    $sysoPath = "${sysoPrefix}_windows_amd64.syso"
    $ldflags = "-s -w -H=windowsgui -extldflags=-Wl,--subsystem,windows"
    if ($env:VERSION) {
        $ver = $env:VERSION.TrimStart('v')
        $ldflags = "$ldflags -X github.com/mrpoundsign/PoundDeck/internal/version.Version=$ver"
    }
    try {
        New-WinResSyso -OutPrefix $sysoPrefix -ProductName "PoundDeck" -Description "PoundDeck Macro & Stream Deck Studio" -OriginalFilename "pounddeck.exe"
        & $GoExe build -trimpath -ldflags="$ldflags" -o $outPath ./cmd/pounddeck
        if ($LASTEXITCODE -eq 0) {
            Write-Host "[OK] Successfully built $outPath" -ForegroundColor Green
        } else {
            Write-Error "Failed to build pounddeck.exe."
        }
    } finally {
        Remove-Item $sysoPath -Force -ErrorAction SilentlyContinue
    }
}

function Run-Tests {
    Write-Host "`n==> Running unit and headless UI tests..." -ForegroundColor Cyan
    Enable-CGoToolchain
    & $GoExe test -count=1 -v ./cmd/... ./internal/... ./pkg/...
    if ($LASTEXITCODE -eq 0) {
        Write-Host "[OK] All tests passed!" -ForegroundColor Green
    } else {
        Write-Error "Tests failed."
    }
}

function Run-Check {
    Write-Host "`n==> Checking goreleaser configuration..." -ForegroundColor Cyan
    $goreleaser = Find-Tool "goreleaser" @(
        (Get-ChildItem "$env:LOCALAPPDATA\Microsoft\WinGet\Packages\goreleaser*" -Recurse -Filter "goreleaser.exe" -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty FullName)
    )
    if (-not $goreleaser) {
        Write-Error "goreleaser not found in PATH."
    }
    & $goreleaser check
    if ($LASTEXITCODE -ne 0) {
        Write-Error "goreleaser check failed."
    }
}

function Run-Lint {
    Write-Host "`n==> Verifying code modernizations (go fix -diff)..." -ForegroundColor Cyan
    Enable-CGoToolchain
    $fixDiff = & $GoExe fix -diff ./...
    if ($LASTEXITCODE -ne 0) {
        Write-Error "go fix -diff failed."
    }
    if ($fixDiff) {
        Write-Host $fixDiff
        Write-Error "Unfixed code modernization diffs found. Run 'go fix ./...' to apply them."
    }
    Write-Host "[OK] Code modernizations verified!" -ForegroundColor Green

    Write-Host "`n==> Running golangci-lint-v2..." -ForegroundColor Cyan
    $linter = Find-Tool "golangci-lint-v2" @(
        (Join-Path $env:USERPROFILE "go\bin\golangci-lint-v2.exe")
    )
    if (-not $linter) {
        Write-Error "golangci-lint-v2 not found in PATH or ~/go/bin."
    }
    & $linter run ./...
    if ($LASTEXITCODE -eq 0) {
        Write-Host "[OK] Lint passed!" -ForegroundColor Green
    } else {
        Write-Error "Lint checks failed."
    }
}

function Clean-Artifacts {
    Write-Host "`n==> Cleaning build artifacts..." -ForegroundColor Yellow
    Remove-Item pounddeck.exe -ErrorAction SilentlyContinue
    if (Test-Path $BuildDir) { Remove-Item $BuildDir -Recurse -Force }
    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    if (Test-Path "dist") { Remove-Item "dist" -Recurse -Force }
    Write-Host "[OK] Cleaned." -ForegroundColor Green
}

switch ($Target) {
    "all"   { Run-Lint; Run-Tests; Build-GUI }
    "gui"   { Build-GUI }
    "test"  { Run-Tests }
    "lint"  { Run-Lint }
    "check" { Run-Check }
    "clean" { Clean-Artifacts }
}
