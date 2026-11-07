package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	eebusapi "github.com/enbility/eebus-go/api"
	"github.com/enbility/eebus-go/service"
	shipapi "github.com/enbility/ship-go/api"
	shipcert "github.com/enbility/ship-go/cert"
	spinemodel "github.com/enbility/spine-go/model"
)

const (
	ClientVersion = "1.3.0"
	CerboSKI      = "f7d9fcea314614a12e3b5d0dcd20605b55f2c3e6"

	CertFile = "client_cert.pem"
	KeyFile  = "client_key.pem"
)

// Globale Variable fuer die maximale PV-Leistung (Standard: 10.000 W)
var maxPvPower float64 = 10000.0

type ClientHandler struct {
	connectedSKI string
}

func (h *ClientHandler) RemoteSKIConnected(srv eebusapi.ServiceInterface, ski string) {
	log.Printf("\n[VERBUNDEN] SHIP-Handshake mit Cerbo erfolgreich! SKI: %s\n> ", ski)
	h.connectedSKI = ski
}

func (h *ClientHandler) RemoteSKIDisconnected(srv eebusapi.ServiceInterface, ski string) {
	log.Printf("\n[TRENNUNG] Verbindung zu Cerbo verloren: %s\n> ", ski)
	if h.connectedSKI == ski {
		h.connectedSKI = ""
	}
}

func (h *ClientHandler) VisibleRemoteServicesUpdated(srv eebusapi.ServiceInterface, entries []shipapi.RemoteService) {
	for _, entry := range entries {
		log.Printf("\n[mDNS] Gefunden: %s (%s)\n> ", entry.Ski, entry.Identifier)
	}
}

func (h *ClientHandler) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	log.Printf("\n[PAIRING STATUS] %s -> %v\n> ", ski, detail.State())
}

func (h *ClientHandler) ServiceShipIDUpdate(ski string, shipID string) {}

func getOrCreateCertificate() (tls.Certificate, error) {
	if _, err := os.Stat(CertFile); err == nil {
		if _, err := os.Stat(KeyFile); err == nil {
			return tls.LoadX509KeyPair(CertFile, KeyFile)
		}
	}

	cert, err := shipcert.CreateCertificate("TestSteuerbox", "GridOperator", "DE", "TestBox01")
	if err != nil {
		return tls.Certificate{}, err
	}

	certOut, err := os.Create(CertFile)
	if err == nil {
		pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
		certOut.Close()
	}

	if ecKey, ok := cert.PrivateKey.(*ecdsa.PrivateKey); ok {
		keyOut, err := os.OpenFile(KeyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err == nil {
			b, err := x509.MarshalECPrivateKey(ecKey)
			if err == nil {
				pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: b})
			}
			keyOut.Close()
		}
	}

	return cert, nil
}

func sendLimit(srv eebusapi.ServiceInterface, ski string, limitIdValue uint, watt float64) {
	if ski == "" {
		fmt.Println("[!] FEHLER: Noch nicht mit dem Cerbo verbunden.")
		return
	}

	remoteDevice := srv.LocalDevice().RemoteDeviceForSki(ski)
	if remoteDevice == nil {
		fmt.Println("[!] FEHLER: Cerbo noch nicht im lokalen SPINE-Cache.")
		return
	}

	localEntity := srv.LocalDevice().EntityForType(spinemodel.EntityTypeTypeCEM)
	if localEntity == nil {
		fmt.Println("[!] FEHLER: Lokale CEM Entity nicht initialisiert.")
		return
	}
	clientFeature := localEntity.FeatureOfTypeAndRole(spinemodel.FeatureTypeTypeLoadControl, spinemodel.RoleTypeClient)
	if clientFeature == nil {
		fmt.Println("[!] FEHLER: Lokales LoadControl Client-Feature fehlt.")
		return
	}

	targetEntity := remoteDevice.Entity([]spinemodel.AddressEntityType{1})
	if targetEntity == nil {
		fmt.Println("[!] FEHLER: Remote CEM Entity [1] nicht gefunden.")
		return
	}
	serverFeature := targetEntity.FeatureOfTypeAndRole(spinemodel.FeatureTypeTypeLoadControl, spinemodel.RoleTypeServer)
	if serverFeature == nil {
		fmt.Println("[!] FEHLER: Remote LoadControl Server-Feature [1]:1 nicht gefunden.")
		return
	}

	featureType := spinemodel.FeatureTypeTypeLoadControl

	_, errSub := remoteDevice.Sender().Subscribe(clientFeature.Address(), serverFeature.Address(), featureType)
	if errSub != nil {
		fmt.Printf("[!] Subscription fehlgeschlagen: %v\n", errSub)
	}
	
	_, errBind := remoteDevice.Sender().Bind(clientFeature.Address(), serverFeature.Address(), featureType)
	if errBind != nil {
		fmt.Printf("[!] Binding fehlgeschlagen: %v\n", errBind)
	}

	time.Sleep(500 * time.Millisecond)

	numVal := spinemodel.NumberType(int64(watt))
	scaleVal := spinemodel.ScaleType(0)
	scaledVal := spinemodel.ScaledNumberType{
		Number: &numVal,
		Scale:  &scaleVal,
	}

	limitId := spinemodel.LoadControlLimitIdType(limitIdValue)
	limitData := &spinemodel.LoadControlLimitListDataType{
		LoadControlLimitData: []spinemodel.LoadControlLimitDataType{
			{
				LimitId: &limitId,
				Value:   &scaledVal,
			},
		},
	}

	fnType := spinemodel.FunctionTypeLoadControlLimitListData
	cmd := spinemodel.CmdType{
		Function:                 &fnType,
		LoadControlLimitListData: limitData,
	}

	msgCounter, err := remoteDevice.Sender().Write(clientFeature.Address(), serverFeature.Address(), cmd)
	if err != nil {
		fmt.Printf("[!] Senden fehlgeschlagen: %v\n", err)
	} else {
		typText := "Netzbezug (§ 14a)"
		if limitIdValue == 2 {
			typText = "PV-Einspeisung (EEG)"
		}
		fmt.Printf("[✓] Signal gesendet (MsgCounter: %v): %.0f Watt (%s)\n", msgCounter, watt, typText)
	}
}

func printMenu() {
	fmt.Println("\n==========================================")
	fmt.Printf("  EEBUS § 14a & EEG Steuerbox v%s  \n", ClientVersion)
	fmt.Println("==========================================")
	fmt.Println(" [1] Verbindungsstatus prüfen")
	fmt.Println(" --- § 14a EnWG (Verbrauch) ---")
	fmt.Println(" [2] § 14a Dimmung SENDEN (4.200 W)")
	fmt.Println(" [3] § 14a Dimmung AUFHEBEN (30.000 W)")
	fmt.Println(" --- EEG (PV-Einspeisung) ---")
	fmt.Printf(" [4] EEG PV 100 %% (%.0f W)\n", maxPvPower)
	fmt.Printf(" [5] EEG PV  60 %% (%.0f W)\n", maxPvPower*0.6)
	fmt.Printf(" [6] EEG PV  30 %% (%.0f W)\n", maxPvPower*0.3)
	fmt.Println(" [7] EEG PV   0 % (0 W)")
	fmt.Println(" --- Einstellungen ---")
	fmt.Println(" [8] Max. PV-Leistung (100 %) ändern")
	fmt.Println(" [q] Beenden")
	fmt.Println("==========================================")
	fmt.Print("Auswahl > ")
}

func main() {
	log.Printf("Starte Steuerbox-Simulator Version %s...", ClientVersion)

	tlsCert, err := getOrCreateCertificate()
	if err != nil {
		log.Fatalf("Zertifikatsfehler: %v", err)
	}

	leaf, _ := x509.ParseCertificate(tlsCert.Certificate[0])
	clientSKI, _ := shipcert.SkiFromCertificate(leaf)
	log.Printf("Eigener Steuerbox SKI: %s", clientSKI)

	config, err := eebusapi.NewConfiguration(
		"GridOperator",
		"GridOperator",
		"ControlBox",
		"9999999999",
		spinemodel.DeviceTypeTypeEnergyManagementSystem,
		[]spinemodel.EntityTypeType{spinemodel.EntityTypeTypeCEM},
		4713,
		tlsCert,
		10*time.Minute,
	)
	if err != nil {
		log.Fatalf("Konfiguration: %v", err)
	}

	handler := &ClientHandler{}
	srv := service.NewService(config, handler)
	if err := srv.Setup(); err != nil {
		log.Fatalf("Setup: %v", err)
	}

	localEntity := srv.LocalDevice().EntityForType(spinemodel.EntityTypeTypeCEM)
	if localEntity != nil {
		f := localEntity.GetOrAddFeature(
			spinemodel.FeatureTypeTypeLoadControl,
			spinemodel.RoleTypeClient,
		)
		f.AddFunctionType(spinemodel.FunctionTypeLoadControlLimitListData, false, false)
		log.Println("[EEBUS] Lokales LoadControl Client Feature registriert.")
	}

	srv.Start()
	defer srv.Shutdown()

	srv.RegisterRemoteSKI(CerboSKI)

	scanner := bufio.NewScanner(os.Stdin)
	for {
		printMenu()
		if !scanner.Scan() {
			break
		}
		choice := strings.TrimSpace(scanner.Text())

		switch choice {
		case "1":
			if handler.connectedSKI != "" {
				fmt.Printf("\n[OK] Verbunden mit: %s\n", handler.connectedSKI)
			} else {
				fmt.Println("\n[INFO] Noch keine Verbindung aktiv. Warte auf SHIP-Handshake...")
			}
		case "2":
			sendLimit(srv, CerboSKI, 1, 4200.0)
		case "3":
			sendLimit(srv, CerboSKI, 1, 30000.0)
		case "4":
			sendLimit(srv, CerboSKI, 2, maxPvPower)
		case "5":
			sendLimit(srv, CerboSKI, 2, maxPvPower*0.6)
		case "6":
			sendLimit(srv, CerboSKI, 2, maxPvPower*0.3)
		case "7":
			sendLimit(srv, CerboSKI, 2, 0.0)
		case "8":
			fmt.Print("Neue max. PV-Leistung in Watt (100 %) eingeben: ")
			if scanner.Scan() {
				if val, err := strconv.ParseFloat(strings.TrimSpace(scanner.Text()), 64); err == nil && val > 0 {
					maxPvPower = val
					fmt.Printf("[OK] Max. PV-Leistung auf %.0f W gesetzt.\n", maxPvPower)
				} else {
					fmt.Println("[!] Ungültige Eingabe.")
				}
			}
		case "q", "Q":
			return
		}
	}
}