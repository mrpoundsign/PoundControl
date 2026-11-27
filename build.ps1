# PoundControl Build Script (PowerShell)
# Usage:
#   .\build.ps1                  # Run lint, tests, and build Desktop Studio GUI
#   .\build.ps1 -Target gui      # Build Desktop Studio GUI (with Zig CGo) -> build\bin\poundcontrol.exe
#   .\build.ps1 -Target installer # Build Windows Guided Setup Installer -> build\bin\PoundControl-Setup.exe
#   .\build.ps1 -Target wasm     # Build example WebAssembly plugins (WASI reactor) -> build\bin\*.wasm
#   .\build.ps1 -Target android  # Test & build Android Companion APK -> build\bin\poundcontrol-android.apk
#   .\build.ps1 -Target test     # Run all unit & headless UI tests
#   .\build.ps1 -Target lint     # Run go fix -diff & golangci-lint-v2
#   .\build.ps1 -Target clean    # Clean build artifacts

param(
    [ValidateSet("all", "gui", "installer", "wasm", "android", "test", "lint", "clean")]
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
    Write-Host "`n==> Building PoundControl Desktop Studio GUI (with CGo)..." -ForegroundColor Cyan
    Enable-CGoToolchain

    if (-not (Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir | Out-Null }
    $outPath = Join-Path $BinDir "poundcontrol.exe"
    $sysoPrefix = Join-Path $PSScriptRoot "cmd\poundcontrol\rsrc"
    $sysoPath = "${sysoPrefix}_windows_amd64.syso"
    $ldflags = "-s -w -H=windowsgui -extldflags=-Wl,--subsystem,windows"
    if ($env:VERSION) {
        $ver = $env:VERSION.TrimStart('v')
        $ldflags = "$ldflags -X github.com/mrpoundsign/PoundControl/internal/version.Version=$ver"
    }
    try {
        New-WinResSyso -OutPrefix $sysoPrefix -ProductName "PoundControl" -Description "PoundControl Macro & Stream Deck Studio" -OriginalFilename "poundcontrol.exe"
        & $GoExe build -trimpath -ldflags="$ldflags" -o $outPath ./cmd/poundcontrol
        if ($LASTEXITCODE -eq 0) {
            Write-Host "[OK] Successfully built $outPath" -ForegroundColor Green
        } else {
            Write-Error "Failed to build poundcontrol.exe."
        }
    } finally {
        Remove-Item $sysoPath -Force -ErrorAction SilentlyContinue
    }
}

function Build-Installer {
    $guiExe = Join-Path $BinDir "poundcontrol.exe"
    if (-not (Test-Path $guiExe)) {
        Build-GUI
    }

    Write-Host "`n==> Building PoundControl Windows Guided Setup Installer..." -ForegroundColor Cyan
    Enable-CGoToolchain

    $payloadDir = Join-Path $PSScriptRoot "cmd\installer\payload"
    if (-not (Test-Path $payloadDir)) { New-Item -ItemType Directory -Path $payloadDir | Out-Null }

    $cleanupPayload = {
        Get-ChildItem -Path $payloadDir -Force | Where-Object { $_.Name -ne "placeholder.txt" } | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
    }

    $sysoPrefix = Join-Path $PSScriptRoot "cmd\installer\rsrc"
    $sysoPath = "${sysoPrefix}_windows_amd64.syso"
    try {
        & $cleanupPayload
        Copy-Item $guiExe (Join-Path $payloadDir "poundcontrol.exe") -Force
        if (Test-Path (Join-Path $PSScriptRoot "LICENSE")) { Copy-Item (Join-Path $PSScriptRoot "LICENSE") (Join-Path $payloadDir "LICENSE") -Force }
        if (Test-Path (Join-Path $PSScriptRoot "README.md")) { Copy-Item (Join-Path $PSScriptRoot "README.md") (Join-Path $payloadDir "README.md") -Force }

        $ldflags = "-s -w -H=windowsgui -extldflags=-Wl,--subsystem,windows"
        if ($env:VERSION) {
            $ver = $env:VERSION.TrimStart('v')
            $ldflags = "$ldflags -X main.AppVersion=$ver"
        }

        New-WinResSyso -OutPrefix $sysoPrefix -ProductName "PoundControl Setup" -Description "PoundControl Guided Setup Installer" -OriginalFilename "PoundControl-Setup.exe"

        $setupOut = Join-Path $BinDir "PoundControl-Setup.exe"
        & $GoExe build -trimpath -ldflags="$ldflags" -o $setupOut ./cmd/installer
        if ($LASTEXITCODE -eq 0) {
            Write-Host "[OK] Successfully built $setupOut" -ForegroundColor Green
        } else {
            Write-Error "Failed to build PoundControl-Setup.exe."
        }
    } finally {
        & $cleanupPayload
        Remove-Item $sysoPath -Force -ErrorAction SilentlyContinue
    }
}

function Build-Wasm {
    Write-Host "`n==> Building example WebAssembly plugins (WASI reactor)..." -ForegroundColor Cyan
    if (-not (Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir | Out-Null }
    $outPath = Join-Path $BinDir "pomodoro.wasm"
    $prevGOOS = $env:GOOS
    $prevGOARCH = $env:GOARCH
    $prevCGO = $env:CGO_ENABLED
    try {
        $env:GOOS = "wasip1"
        $env:GOARCH = "wasm"
        $env:CGO_ENABLED = "0"
        & $GoExe build -buildmode=c-shared -o $outPath ./examples/wasm-pomodoro
        if ($LASTEXITCODE -eq 0) {
            Write-Host "[OK] Successfully built $outPath" -ForegroundColor Green
        } else {
            Write-Error "Failed to build pomodoro.wasm."
        }
    } finally {
        $env:GOOS = $prevGOOS
        $env:GOARCH = $prevGOARCH
        $env:CGO_ENABLED = $prevCGO
    }
}

function Run-Tests {
    Write-Host "`n==> Running unit and headless UI tests..." -ForegroundColor Cyan
    Enable-CGoToolchain
    & $GoExe test -count=1 ./cmd/... ./internal/... ./pkg/...
    if ($LASTEXITCODE -eq 0) {
        Write-Host "[OK] All tests passed!" -ForegroundColor Green
    } else {
        Write-Error "Tests failed."
    }
}

function Run-Lint {
    Write-Host "`n==> Verifying code modernizations (go fix -diff)..." -ForegroundColor Cyan
    Enable-CGoToolchain
    $fixDiff = & $GoExe fix -diff ./cmd/... ./internal/... ./pkg/...
    if ($LASTEXITCODE -ne 0) {
        Write-Error "go fix -diff failed."
    }
    if ($fixDiff) {
        Write-Host $fixDiff
        Write-Error "Unfixed code modernization diffs found. Run 'go fix ./cmd/... ./internal/... ./pkg/...' to apply them."
    }
    Write-Host "[OK] Code modernizations verified!" -ForegroundColor Green

    Write-Host "`n==> Running golangci-lint-v2..." -ForegroundColor Cyan
    $linter = Find-Tool "golangci-lint-v2" @(
        (Join-Path $env:USERPROFILE "go\bin\golangci-lint-v2.exe")
    )
    if (-not $linter) {
        Write-Error "golangci-lint-v2 not found in PATH or ~/go/bin."
    }
    & $linter run ./cmd/... ./internal/... ./pkg/...
    if ($LASTEXITCODE -eq 0) {
        Write-Host "[OK] Lint passed!" -ForegroundColor Green
    } else {
        Write-Error "Lint checks failed."
    }
}

function Clean-Artifacts {
    Write-Host "`n==> Cleaning build artifacts..." -ForegroundColor Yellow
    Remove-Item poundcontrol.exe -ErrorAction SilentlyContinue
    if (Test-Path $BuildDir) { Remove-Item $BuildDir -Recurse -Force }
    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    if (Test-Path "dist") { Remove-Item "dist" -Recurse -Force }
    Write-Host "[OK] Cleaned." -ForegroundColor Green
}

function Build-Android {
    Write-Host "`n==> Testing & Building PoundControl Android Companion APK..." -ForegroundColor Cyan
    $androidDir = Join-Path $PSScriptRoot "mobile\android"
    $gradlew = Join-Path $androidDir "gradlew.bat"
    if (-not (Test-Path $gradlew)) {
        Write-Error "Gradle wrapper not found at $gradlew."
    }

    $javaHome = $env:JAVA_HOME
    if (-not $javaHome -or -not (Test-Path (Join-Path $javaHome "bin\java.exe"))) {
        $jbr = "C:\Program Files\Android\Android Studio\jbr"
        if (Test-Path (Join-Path $jbr "bin\java.exe")) {
            $javaHome = $jbr
        } else {
            $javaHome = Get-ChildItem "C:\Program Files\Eclipse Adoptium\*", "C:\Program Files\Java\*", "C:\Program Files\Microsoft\jdk*" -Directory -ErrorAction SilentlyContinue |
                Where-Object { Test-Path (Join-Path $_.FullName "bin\java.exe") } |
                Select-Object -First 1 -ExpandProperty FullName
        }
    }
    if (-not $javaHome) {
        Write-Error "JDK not found. Set JAVA_HOME or install Android Studio / JDK 17+."
    }

    $sdkHome = $env:ANDROID_HOME
    if (-not $sdkHome -or -not (Test-Path $sdkHome)) {
        if ($env:ANDROID_SDK_ROOT -and (Test-Path $env:ANDROID_SDK_ROOT)) {
            $sdkHome = $env:ANDROID_SDK_ROOT
        } elseif (Test-Path (Join-Path $env:LOCALAPPDATA "Android\Sdk")) {
            $sdkHome = Join-Path $env:LOCALAPPDATA "Android\Sdk"
        }
    }
    if (-not $sdkHome -or -not (Test-Path $sdkHome)) {
        Write-Error "Android SDK not found. Set ANDROID_HOME or install Android SDK platform tools."
    }

    Write-Host "Using JAVA_HOME=$javaHome" -ForegroundColor DarkGray
    Write-Host "Using ANDROID_HOME=$sdkHome" -ForegroundColor DarkGray

    $prevJava = $env:JAVA_HOME
    $prevAndroid = $env:ANDROID_HOME
    $prevSdkRoot = $env:ANDROID_SDK_ROOT
    try {
        $env:JAVA_HOME = $javaHome
        $env:ANDROID_HOME = $sdkHome
        $env:ANDROID_SDK_ROOT = $sdkHome
        Push-Location $androidDir
        try {
            & $gradlew :protocol:testDebugUnitTest :app:testDebugUnitTest :wear:testDebugUnitTest :app:assembleDebug :wear:assembleDebug --no-daemon
            if ($LASTEXITCODE -ne 0) {
                Write-Error "Android unit tests or APK build failed."
            }
        } finally {
            Pop-Location
        }
    } finally {
        $env:JAVA_HOME = $prevJava
        $env:ANDROID_HOME = $prevAndroid
        $env:ANDROID_SDK_ROOT = $prevSdkRoot
    }

    $apkSrc = Join-Path $androidDir "app\build\outputs\apk\debug\app-debug.apk"
    if (-not (Test-Path $apkSrc)) {
        Write-Error "Expected APK not found at $apkSrc."
    }
    $wearApkSrc = Join-Path $androidDir "wear\build\outputs\apk\debug\wear-debug.apk"
    if (-not (Test-Path $wearApkSrc)) {
        Write-Error "Expected Wear APK not found at $wearApkSrc."
    }
    if (-not (Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir | Out-Null }
    $apkOut = Join-Path $BinDir "poundcontrol-android.apk"
    $wearApkOut = Join-Path $BinDir "poundcontrol-wear.apk"
    Copy-Item -Path $apkSrc -Destination $apkOut -Force
    Copy-Item -Path $wearApkSrc -Destination $wearApkOut -Force
    Write-Host "[OK] Successfully built $apkOut" -ForegroundColor Green
    Write-Host "[OK] Successfully built $wearApkOut" -ForegroundColor Green
}

switch ($Target) {
    "all"       { Run-Lint; Run-Tests; Build-GUI }
    "gui"       { Build-GUI }
    "installer" { Build-Installer }
    "wasm"      { Build-Wasm }
    "android"   { Build-Android }
    "test"      { Run-Tests }
    "lint"      { Run-Lint }
    "clean"     { Clean-Artifacts }
}
