package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	eebusapi "github.com/enbility/eebus-go/api"
	"github.com/enbility/eebus-go/service"
	shipapi "github.com/enbility/ship-go/api"
	shipcert "github.com/enbility/ship-go/cert"
	spinemodel "github.com/enbility/spine-go/model"
)

const (
	CertDir      = "/data/apps/eebus/cert"
	MqttBroker   = "tcp://127.0.0.1:1883"
	ServiceName  = "VictronCerboEEBUS"
	Manufacturer = "VictronCommunity"
	DeviceModel  = "CerboGX-EEBUS"
)

type ServiceHandler struct {
	mqttClient mqtt.Client
}

func (h *ServiceHandler) RemoteSKIConnected(srv eebusapi.ServiceInterface, ski string) {
	log.Printf("[EEBUS] Steuerbox verbunden: %s", ski)
}

func (h *ServiceHandler) RemoteSKIDisconnected(srv eebusapi.ServiceInterface, ski string) {
	log.Printf("[EEBUS] Steuerbox getrennt: %s", ski)
}

func (h *ServiceHandler) VisibleRemoteServicesUpdated(srv eebusapi.ServiceInterface, entries []shipapi.RemoteService) {
	for _, entry := range entries {
		log.Printf("[EEBUS mDNS] Gefundenes Geraet: SKI=%s", entry.Ski)
	}
}

func (h *ServiceHandler) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	log.Printf("[EEBUS Pairing] Update fuer SKI %s", ski)
}

func (h *ServiceHandler) ServiceShipIDUpdate(ski string, shipID string) {
	log.Printf("[EEBUS SHIP] Update fuer SKI %s -> ShipID %s", ski, shipID)
}

func ensureCertificate(certFile, keyFile string) (tls.Certificate, error) {
	if _, err := os.Stat(certFile); err == nil {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}

	log.Println("[EEBUS] Erstelle neues X.509 ECDSA Zertifikat...")
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName:   ServiceName,
			Organization: []string{Manufacturer},
			Country:      []string{"DE"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	certOut, err := os.Create(certFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return tls.Certificate{}, err
	}

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyOut, err := os.Create(keyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	defer keyOut.Close()
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		return tls.Certificate{}, err
	}

	return tls.LoadX509KeyPair(certFile, keyFile)
}

func main() {
	log.Println("Starte EEBUS Service für Cerbo GX...")

	// 1. MQTT Client verbinden
	mqttOpts := mqtt.NewClientOptions().AddBroker(MqttBroker).SetClientID("cerbo-eebus-bridge")
	mqttOpts.SetAutoReconnect(true)
	mqttClient := mqtt.NewClient(mqttOpts)
	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		log.Printf("[MQTT] Lokaler Broker nicht erreichbar: %v", token.Error())
	} else {
		log.Println("[MQTT] Verbindung zu Cerbo-Broker aktiv.")
	}

	handler := &ServiceHandler{mqttClient: mqttClient}

	// 2. Zertifikat vorbereiten
	if err := os.MkdirAll(CertDir, 0755); err != nil {
		log.Fatalf("Zertifikatsordner fehlerhaft: %v", err)
	}
	certFile := filepath.Join(CertDir, "cert.crt")
	keyFile := filepath.Join(CertDir, "cert.key")

	tlsCert, err := ensureCertificate(certFile, keyFile)
	if err != nil {
		log.Fatalf("Zertifikatsinitialisierung fehlgeschlagen: %v", err)
	}

	leaf, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		log.Fatalf("Zertifikat fehlerhaft: %v", err)
	}
	ski, _ := shipcert.SkiFromCertificate(leaf)
	log.Printf("[EEBUS] Eigener Cerbo SKI: %s", ski)

	// 3. Konfigurationsobjekt als *eebusapi.Configuration erstellen
	entityTypes := []spinemodel.EntityTypeType{
		spinemodel.EntityTypeTypeCEM,
	}

	config, err := eebusapi.NewConfiguration(
		Manufacturer,
		Manufacturer,
		DeviceModel,
		"1000000001",
		spinemodel.DeviceTypeTypeEnergyManagementSystem,
		entityTypes,
		4712,
		tlsCert,
		10*time.Minute,
	)
	if err != nil {
		log.Fatalf("Fehler bei Service-Konfiguration: %v", err)
	}

	srv := service.NewService(config, handler)
	if err := srv.Setup(); err != nil {
		log.Fatalf("EEBUS Setup fehlgeschlagen: %v", err)
	}

	srv.Start()
	log.Println("[EEBUS] Service lauscht auf Port 4712. mDNS aktiv.")

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)
	<-stopChan

	log.Println("Beende EEBUS Service...")
	srv.Shutdown()
	mqttClient.Disconnect(250)
	fmt.Println("Beendet.")
}
