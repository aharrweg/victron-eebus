# Build script for EEBUS Steuerbox Simulator (client.go) - Windows only

$ErrorActionPreference = "Stop"

Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "   EEBUS Windows Client Build             " -ForegroundColor Cyan
Write-Host "==========================================" -ForegroundColor Cyan

# 1. Pruefen, ob Go installiert ist
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Error "Go ist nicht im PATH vorhanden. Bitte Go installieren oder Umgebungsvariablen pruefen."
    exit 1
}

# 2. Dependencies aktualisieren / herunterladen
Write-Host "`n[1/2] Aktualisiere Go Module Dependencies..." -ForegroundColor Yellow
go mod tidy
if ($LASTEXITCODE -ne 0) {
    Write-Error "Fehler bei 'go mod tidy'."
    exit $LASTEXITCODE
}

# 3. Kompilieren fuer Windows (x64)
Write-Host "`n[2/2] Kompiliere eebus-client.exe (Windows amd64)..." -ForegroundColor Yellow

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
Remove-Item Env:GOARM -ErrorAction SilentlyContinue

go build -ldflags="-s -w" -o "eebus-client.exe" client.go

if ($LASTEXITCODE -eq 0) {
    Write-Host "`n[OK] Build erfolgreich: eebus-client.exe" -ForegroundColor Green
} else {
    Write-Error "Build fehlgeschlagen."
    exit $LASTEXITCODE
}
