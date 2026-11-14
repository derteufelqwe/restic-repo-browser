<#
.SYNOPSIS
  Builds restic-browser for every release platform into .\build.
.EXAMPLE
  .\build.ps1                      # version "dev", all platforms
  .\build.ps1 -Version 1.2.3
  .\build.ps1 -Targets linux/amd64 # only some platforms
#>
param(
    [string]$Version = "dev",
    [string[]]$Targets = @("linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64", "darwin/amd64", "darwin/arm64")
)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$out = "build"
if (Test-Path $out) { Remove-Item -Recurse -Force $out }
New-Item -ItemType Directory -Path $out | Out-Null

$saved = @{ GOOS = $env:GOOS; GOARCH = $env:GOARCH; CGO_ENABLED = $env:CGO_ENABLED }
try {
    $env:CGO_ENABLED = "0"
    foreach ($target in $Targets) {
        $os, $arch = $target -split "/"
        $ext = if ($os -eq "windows") { ".exe" } else { "" }
        $file = "$out/restic-browser-$os-$arch$ext"
        Write-Host "building $file"
        $env:GOOS = $os
        $env:GOARCH = $arch
        go build -trimpath -ldflags "-s -w -X main.releaseVersion=$Version" -o $file ./cmd/restic-browser
        if ($LASTEXITCODE -ne 0) { throw "build failed for $target" }
    }
} finally {
    foreach ($name in $saved.Keys) {
        if ($null -eq $saved[$name]) { Remove-Item "Env:$name" -ErrorAction SilentlyContinue }
        else { Set-Item "Env:$name" $saved[$name] }
    }
}
Write-Host "done: $out/"
