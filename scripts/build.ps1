# Builds everything: agent for the box (linux/arm, GOARM=7: arm64 builds crash with SIGILL on this kernel),
# the probe, and the PC viewer. Output: agent\build\. The BLE bridge APK is built by bmsbridge\build.ps1.
$ErrorActionPreference = 'Continue'
$go = 'C:\Program Files\Go\bin\go.exe'
Push-Location (Join-Path $PSScriptRoot '..\agent')
try {
    $env:GOOS = ''; $env:GOARCH = ''
    & $go test ./internal/...
    if ($LASTEXITCODE) { throw 'tests failed' }
    $env:CGO_ENABLED = '0'; $env:GOOS = 'linux'; $env:GOARCH = 'arm'; $env:GOARM = '7'
    & $go vet ./...
    & $go build -trimpath -ldflags='-s -w' -o build/mxg-arm ./cmd/mxg
    & $go build -trimpath -ldflags='-s -w' -o build/mustprobe-arm ./cmd/mustprobe
    $env:GOOS = 'windows'; $env:GOARCH = 'amd64'
    & $go build -trimpath -ldflags='-s -w' -o build/mxgview.exe ./cmd/mxgview
    Get-ChildItem build | Select-Object Name, Length, LastWriteTime
} finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:GOARM, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}
