# Find Scoop directory
$scoopDir = ""
if ($env:SCOOP) {
    $scoopDir = $env:SCOOP
} elseif (Test-Path "$env:USERPROFILE\scoop") {
    $scoopDir = "$env:USERPROFILE\scoop"
} elseif (Test-Path "D:\scoop") { # Check for custom scoop directory
    $scoopDir = "D:\scoop"
} else {
    Write-Error "Scoop directory not found. Please ensure Scoop is installed or set the SCOOP environment variable, or check common locations like $env:USERPROFILE\scoop or D:\scoop."
    exit 1
}

# Find zlib directory within Scoop
$zlibDir = Join-Path $scoopDir "apps\zlib\current"
if (-not (Test-Path $zlibDir)) {
    Write-Error "zlib package not found in Scoop apps directory: $zlibDir"
    Write-Host "Please ensure zlib is installed via Scoop: scoop install zlib"
    exit 1
}

# Set environment variables pointing to the zlib installation
$env:ZLIB_HOME = $zlibDir
$env:CGO_CPPFLAGS = "-I$($env:ZLIB_HOME)\include"
$env:CGO_LDFLAGS = "-L$($env:ZLIB_HOME)\lib"
# Add zlib bin to PATH if it exists (some versions might not have a bin dir)
$zlibBinDir = Join-Path $zlibDir "bin"
if (Test-Path $zlibBinDir) {
    # Check if the path is already present (case-insensitive)
    $currentPathArray = ($env:PATH -split ';') | Where-Object { $_ -ne '' }
    if ($zlibBinDir -notin $currentPathArray) {
        # Prepend only if not already in PATH
        $env:PATH = "$zlibBinDir;$($env:PATH)"
    }
}

# Clean up PATH duplicates
$uniquePaths = ($env:PATH -split ';') | Where-Object { $_ -ne '' } | Select-Object -Unique
$env:PATH = $uniquePaths -join ';'

Write-Host "Environment variables set:"
Write-Host "  ZLIB_HOME      = $env:ZLIB_HOME"
Write-Host "  CGO_CPPFLAGS   = $env:CGO_CPPFLAGS"
Write-Host "  CGO_LDFLAGS    = $env:CGO_LDFLAGS"
Write-Host "  PATH (updated) = $env:PATH"

# Ensure latest code
Write-Host "Ensuring latest code via git pull..."
git pull
if ($LASTEXITCODE -ne 0) {
    Write-Warning "git pull failed with exit code $LASTEXITCODE. Proceeding with potentially outdated code."
} else {
    Write-Host "Git pull completed successfully."
}


# Tidy dependencies based on go.mod
Write-Host "Running go mod tidy..."
go mod tidy

# Check the exit code of go mod tidy
if ($LASTEXITCODE -ne 0) {
    Write-Error "go mod tidy failed with exit code $LASTEXITCODE"
    exit $LASTEXITCODE
} else {
    Write-Host "go mod tidy completed successfully."
}

# Optional: Update vendor directory if it exists
if (Test-Path ".\vendor") {
    Write-Host "Vendor directory found. Running go mod vendor..."
    go mod vendor
    if ($LASTEXITCODE -ne 0) {
        Write-Error "go mod vendor failed with exit code $LASTEXITCODE"
        exit $LASTEXITCODE
    } else {
        Write-Host "go mod vendor completed successfully."
    }
}


# Then run your build command
Write-Host "Running go build..."
go build

# Check the exit code of go build
if ($LASTEXITCODE -ne 0) {
    Write-Error "go build failed with exit code $LASTEXITCODE"
    exit $LASTEXITCODE
} else {
    Write-Host "go build completed successfully."
    # Play a system beep sound
    [System.Media.SystemSounds]::Beep.Play()
} 