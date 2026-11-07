# Build- und Deploy-Skript fuer victron-eebus (Cerbo GX)
# Kompiliert das Go-Projekt fuer Linux ARMv7 und kopiert es per SCP auf den Cerbo

param (
    [string]$CerboIP = "192.168.178.37",
    [string]$CerboUser = "root",
    [string]$RemotePath = "/data/apps/eebus/eebus-cerbo",
    [switch]$RestartService
)

$ErrorActionPreference = "Stop"

Write-Host "==========================================" -ForegroundColor Cyan
Write-Host "   Victron Cerbo EEBUS - Build & Deploy   " -ForegroundColor Cyan
Write-Host "==========================================" -ForegroundColor Cyan

# 1. Pruefen, ob Go installiert ist
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Error "Go ist nicht im PATH vorhanden. Bitte Go-Installation pruefen."
    exit 1
}

# 2. Dependencies aufraeumen
Write-Host "`n[1/4] Aktualisiere Go Module..." -ForegroundColor Yellow
go mod tidy
if ($LASTEXITCODE -ne 0) {
    Write-Error "Fehler bei 'go mod tidy'."
    exit $LASTEXITCODE
}

# 3. Kompilieren fuer Cerbo GX (Linux ARMv7 Cortex-A7)
Write-Host "`n[2/4] Kompiliere eebus-cerbo (Linux armv7)..." -ForegroundColor Yellow

$env:CGO_ENABLED = "0"
$env:GOOS = "linux"
$env:GOARCH = "arm"
$env:GOARM = "7"

go build -ldflags="-s -w" -o eebus-cerbo main.go

if ($LASTEXITCODE -ne 0) {
    Write-Error "Build fehlgeschlagen."
    exit $LASTEXITCODE
}
Write-Host "[OK] Binary 'eebus-cerbo' erfolgreich gebaut." -ForegroundColor Green

# 4. Binary per SCP auf den Cerbo GX uebertragen
Write-Host "`n[3/4] Uebertrage Binary via SCP nach ${CerboUser}@${CerboIP}:${RemotePath}..." -ForegroundColor Yellow

scp eebus-cerbo "${CerboUser}@${CerboIP}:${RemotePath}"
if ($LASTEXITCODE -ne 0) {
    Write-Error "SCP-Uebertragung fehlgeschlagen. Netzwerk, IP oder SSH-Schluessel pruefen."
    exit $LASTEXITCODE
}
Write-Host "[OK] Datei erfolgreich auf Cerbo GX uebertragen." -ForegroundColor Green

# 5. Berechtigungen setzen (und optional Service neu starten)
Write-Host "`n[4/4] Setze Ausfuehrungsrechte (chmod +x)..." -ForegroundColor Yellow
ssh "${CerboUser}@${CerboIP}" "chmod +x ${RemotePath}"

if ($RestartService) {
    Write-Host "Starte Dienst eebus-cerbo auf Cerbo neu..." -ForegroundColor Yellow
    ssh "${CerboUser}@${CerboIP}" "killall -9 eebus-cerbo 2>/dev/null; nohup ${RemotePath} > /var/log/eebus.log 2>&1 &"
    Write-Host "[OK] Dienst neu gestartet." -ForegroundColor Green
}

Write-Host "`n[OK] Fertig!" -ForegroundColor Green