package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/enbility/eebus-go/service"
	"github.com/enbility/eebus-go/usecases/cem/lpc"
	"github.com/enbility/eebus-go/usecases/cem/lpp"
	shipapi "github.com/enbility/ship-go/api"
	"github.com/enbility/ship-go/cert"
	spineapi "github.com/enbility/spine-go/api"
	"github.com/enbility/spine-go/model"
)

const (
	CertDir             = "/data/apps/eebus/cert"
	MqttBroker          = "tcp://127.0.0.1:1883"
	MqttTopicGridLimit  = "victron/eebus/grid_limit"       // LPC (Verbrauch/Ladung)
	MqttTopicFeedIn     = "victron/eebus/feed_in_limit"   // LPP (PV-Einspeisung)
	ServiceName         = "VictronCerboEEBUS"
	Manufacturer        = "VictronCommunity"
	DeviceModel         = "CerboGX-EEBUS"
)

type LimitPayload struct {
	Type       string    `json:"type"` // "LPC" oder "LPP"
	Active     bool      `json:"active"`
	LimitWatts float64   `json:"limit_watts"`
	DurationS  int64     `json:"duration_seconds"`
	Timestamp  time.Time `json:"timestamp"`
	SKI        string    `json:"source_ski"`
}

type ServiceHandler struct {
	mqttClient mqtt.Client
	lpcUC      *lpc.LPC
	lppUC      *lpp.LPP
}

func (h *ServiceHandler) PublishLimit(topic string, limitType string, limitWatts float64, active bool, duration time.Duration, ski string) {
	payload := LimitPayload{
		Type:       limitType,
		Active:     active,
		LimitWatts: limitWatts,
		DurationS:  int64(duration.Seconds()),
		Timestamp:  time.Now(),
		SKI:        ski,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[MQTT] Fehler beim Serialisieren: %v", err)
		return
	}

	token := h.mqttClient.Publish(topic, 0, true, data)
	token.Wait()
	log.Printf("[EEBUS -> MQTT] %s-Limit: %.0f W (Aktiv: %t) -> %s", limitType, limitWatts, active, topic)
}

// Hilfsfunktion zur Autorisierungsprüfung
func isAuthorized(remoteSki string, allowedList []string) bool {
    for _, s := range allowedList {
        if s == remoteSki {
            return true
        }
    }
    return false
}

func main() {
	log.Println("Starte EEBUS Service (LPC & LPP) fuer Cerbo GX...")

	// 1. MQTT Client initialisieren
	mqttOpts := mqtt.NewClientOptions().AddBroker(MqttBroker).SetClientID("cerbo-eebus-bridge")
	mqttOpts.SetAutoReconnect(true)
	mqttClient := mqtt.NewClient(mqttOpts)
	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		log.Printf("[MQTT] Warnung: Kein lokaler Broker: %v", token.Error())
	} else {
		log.Println("[MQTT] Verbunden mit lokalem Cerbo-Broker.")
	}

	handler := &ServiceHandler{mqttClient: mqttClient}

	// 2. Zertifikat laden / generieren
	if err := os.MkdirAll(CertDir, 0755); err != nil {
		log.Fatalf("Cert-Verzeichnis-Fehler: %v", err)
	}
	certFile := filepath.Join(CertDir, "cert.crt")
	keyFile := filepath.Join(CertDir, "cert.key")
	var tlsCert tls.Certificate

	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		log.Println("[EEBUS] Generiere Zertifikat...")
		c, err := cert.CreateCertificate(ServiceName, "DE", "", Manufacturer)
		if err != nil {
			log.Fatalf("Fehler Cert-Erstellung: %v", err)
		}
		_ = cert.SaveCertificate(certFile, keyFile, c)
		tlsCert = c
	} else {
		c, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			log.Fatalf("Cert-Ladefehler: %v", err)
		}
		tlsCert = c
	}

	leaf, _ := x509.ParseCertificate(tlsCert.Certificate[0])
