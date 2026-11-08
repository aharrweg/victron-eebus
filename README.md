# Victron EEBUS Bridge (§ 14a EnWG \& EEG)

Dieses Projekt bietet eine leichtgewichtige EEBUS-Integration für Victron Energy Systeme (Cerbo GX / Venus OS). Es ermöglicht die normgerechte Leistungsreduzierung von Verbrauchern nach **§ 14a EnWG** (z.B. Wärmepumpen, Wallboxen, Batteriespeicher) sowie die **Wirkleistungsbegrenzung nach EEG** (PV-Abregelung) durch den Netzbetreiber (z.B. ovag Netz GmbH).

## 🌟 Funktionsweise

Das System besteht aus einer Go-basierten Brücke, die direkt auf dem Victron Cerbo GX läuft. Sie kommuniziert über das EEBUS SPINE-Protokoll mit der Steuerbox des Netzbetreibers und übersetzt die Steuerbefehle in standardisierte MQTT-Nachrichten.

Die Brücke trennt sauber zwischen zwei Anwendungsfällen:

* **LimitId 1 (Netzbezug / § 14a EnWG)** -> Publiziert auf MQTT `eebus/grid/limit`
* **LimitId 2 (PV-Einspeisung / EEG)** -> Publiziert auf MQTT `eebus/pv/limit`

## 📁 Projektstruktur

* `/cerbo`: Der eigentliche EEBUS-Server (`main.go`), der auf dem Cerbo GX (Linux ARMv7) ausgeführt wird. Enthält ein Skript zum automatischen Deployment.
* `/client`: Ein Windows-Test-Client (`client.go`). Dieser simuliert eine Steuerbox des Netzbetreibers und sendet auf Knopfdruck Dimm-Vorgaben. Ideal zur Inbetriebnahme und Fehlersuche.
* `nodered_example.json`: Ein fertiger Node-RED Flow zur Integration in das Venus OS.
* `manuell.txt`: Detaillierte Anleitung zur manuellen Einrichtung des Dienstes auf dem Cerbo GX (inkl. Autostart).

## 🚀 Installation \& Build

### Vorbereitung

Du benötigst [Go](https://go.dev/dl/) auf deinem Entwicklungsrechner.

### 1\. Cerbo GX Server installieren

Im Ordner `/cerbo` befindet sich das Skript `deploy_cerbo.ps1`. Es kompiliert den Go-Code für Linux ARMv7 und lädt ihn per SCP direkt auf den Cerbo hoch.
Passe im Skript die IP-Adresse deines Cerbo GX an und führe es aus:

Unter Windows: powershell -ExecutionPolicy Bypass -File .\deploy_cerbo.ps1  

*(Für die manuelle Einrichtung als Hintergrunddienst auf dem Cerbo siehe `manuell.txt`)*

### 2\. Windows Test-Client nutzen

Wechsle in den Ordner `/client` und führe das Build-Skript aus:

Unter Windows: powershell -ExecutionPolicy Bypass -File .\build_client_win.ps1

Anschließend kannst du die `eebus-client.exe` starten, um dich mit dem Cerbo zu verbinden und Testsignale (z.B. 60 % PV-Abregelung oder 4,2 kW Bezugslimit) zu senden.

## 🔗 Node-RED Integration (Venus OS)

Im Hauptverzeichnis befindet sich die Datei `nodered_example.json`. Sie enthält einen fertigen Flow für das Node-RED deines Cerbo GX.
Importiere den Flow einfach über das Menü (Import -> Clipboard).

Der Flow überwacht automatisch die lokalen MQTT-Topics und wandelt die EEBUS-Limits in die entsprechenden D-Bus-Befehle für das Victron ESS um:

* **§ 14a EnWG:** Setzt das AC Input Limit (in Ampere).
* **EEG (PV):** Setzt das Grid Feed-In Limit (`/Settings/CGwacs/MaxFeedInPower` in Watt).

## 🔒 Sicherheitshinweis

Das Projekt generiert beim ersten Start automatisch TLS-Zertifikate für den EEBUS-Handshake (SHIP). Diese `.pem` und `.crt` Dateien werden durch die `.gitignore` ignoriert und sollten **niemals** öffentlich geteilt werden.

## 🛠 Verwendete Bibliotheken

Ein großer Dank geht an das [Enbility-Projekt](https://github.com/enbility) für den hervorragenden Go-EEBUS-Stack (`eebus-go`, `ship-go`, `spine-go`).

## 📄 Lizenz

Dieses Projekt steht unter der MIT-Lizenz.



