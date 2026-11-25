param([string]$Go = 'go', [string]$OutputDirectory = 'dist')
$ErrorActionPreference = 'Stop'
$project = Split-Path -Parent $PSScriptRoot
Push-Location -LiteralPath $project
try {
    npm --prefix web run build
    if ($LASTEXITCODE) { throw 'Workbench build failed' }
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    $dist = New-Item -ItemType Directory -Path $OutputDirectory -Force
    $file = Join-Path $dist.FullName 'Dockroom-Windows-x64.exe'
    & $Go build -trimpath -ldflags '-H=windowsgui' -o $file ./cmd/hub
    if ($LASTEXITCODE) { throw 'Dockroom Windows build failed' }
    $sha = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText((Join-Path $dist.FullName 'SHA256SUMS.txt'), "$sha  Dockroom-Windows-x64.exe`n")
    Write-Host "Windows EXE: $file"
    Write-Host "SHA-256: $sha"
} finally {
    Pop-Location
}
