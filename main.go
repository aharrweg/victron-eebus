package main

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
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
	spineapi "github.com/enbility/spine-go/api"
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
	srv        eebusapi.ServiceInterface
}

func (h *ServiceHandler) publishJSON(topic string, data any) {
	if h.mqttClient == nil || !h.mqttClient.IsConnected() {
		return
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	h.mqttClient.Publish(topic, 0, false, payload)
}

func (h *ServiceHandler) RemoteSKIConnected(srv eebusapi.ServiceInterface, ski string) {
	log.Printf("[EEBUS] Steuerbox verbunden: %s", ski)
	h.publishJSON("eebus/status", map[string]any{
		"connected":  true,
		"remote_ski": ski,
		"timestamp":  time.Now().Format(time.RFC3339),
	})
}

func (h *ServiceHandler) RemoteSKIDisconnected(srv eebusapi.ServiceInterface, ski string) {
	log.Printf("[EEBUS] Steuerbox getrennt: %s", ski)
	h.publishJSON("eebus/status", map[string]any{
		"connected":  false,
		"remote_ski": ski,
		"timestamp":  time.Now().Format(time.RFC3339),
	})
}

func (h *ServiceHandler) VisibleRemoteServicesUpdated(srv eebusapi.ServiceInterface, entries []shipapi.RemoteService) {
	for _, entry := range entries {
		log.Printf("[EEBUS mDNS] Gefundenes Gerät: SKI=%s", entry.Ski)
		if h.srv != nil {
			h.srv.RegisterRemoteSKI(entry.Ski)
		}
	}
}

func (h *ServiceHandler) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	log.Printf("[EEBUS Pairing] Update für SKI %s -> Status: %v", ski, detail.State())
	h.publishJSON("eebus/pairing", map[string]any{
		"ski":       ski,
		"state":     fmt.Sprintf("%v", detail.State()),
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

func (h *ServiceHandler) ServiceShipIDUpdate(ski string, shipID string) {
	h.publishJSON("eebus/ship", map[string]any{
		"ski":       ski,
		"ship_id":   shipID,
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

// SPINE Event Handler für eingehende Dimmvorgaben (§ 14a EnWG)
func (h *ServiceHandler) HandleSpineEvent(payload spineapi.EventPayload) {
	if payload.EventType != spineapi.EventTypeDataChange || payload.ChangeType != spineapi.ElementChangeUpdate {
		return
	}

	// Prüfen, ob es sich um eine Begrenzung (LoadControlLimitListDataType) handelt
	if data, ok := payload.Data.(*spinemodel.LoadControlLimitListDataType); ok && data != nil {
		for _, item := range data.LoadControlLimitData {
			if item.Value != nil {
				limitVal := item.Value.GetValue()
				log.Printf("[EEBUS §14a] Dimmvorgabe empfangen: %.1f W", limitVal)

				h.publishJSON("eebus/grid/limit", map[string]any{
					"active":        limitVal > 0 && limitVal < 25000,
					"power_limit_w": limitVal,
					"timestamp":     time.Now().Format(time.RFC3339),
				})
			}
		}
	}
}

func ensureCertificate(certFile, keyFile string) (tls.Certificate, error) {
	if _, err := os.Stat(certFile); err == nil {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}

	log.Println("[EEBUS] Erstelle neues X.509 ECDSA Zertifikat inklusive SKI...")
	tlsCert, err := shipcert.CreateCertificate(ServiceName, Manufacturer, "DE", ServiceName)
	if err != nil {
		return tls.Certificate{}, err
	}

	certOut, err := os.Create(certFile)
	if err == nil {
		pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: tlsCert.Certificate[0]})
		certOut.Close()
	}

	if ecdsaKey, ok := tlsCert.PrivateKey.(*ecdsa.PrivateKey); ok {
		keyBytes, _ := x509.MarshalECPrivateKey(ecdsaKey)
		keyOut, err := os.Create(keyFile)
		if err == nil {
			pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
			keyOut.Close()
		}
	}

	return tlsCert, nil
}

func main() {
	log.Println("Starte EEBUS Service für Cerbo GX...")

	mqttOpts := mqtt.NewClientOptions().AddBroker(MqttBroker).SetClientID("cerbo-eebus-bridge")
	mqttOpts.SetAutoReconnect(true)
	mqttClient := mqtt.NewClient(mqttOpts)
	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		log.Printf("[MQTT] Fehler: %v", token.Error())
	}

	handler := &ServiceHandler{mqttClient: mqttClient}

	if err := os.MkdirAll(CertDir, 0755); err != nil {
		log.Fatalf("Zertifikatsordner fehlerhaft: %v", err)
	}
	certFile := filepath.Join(CertDir, "cert.crt")
	keyFile := filepath.Join(CertDir, "cert.key")

	tlsCert, err := ensureCertificate(certFile, keyFile)
	if err != nil {
		log.Fatalf("Zertifikatsfehler: %v", err)
	}

	leaf, _ := x509.ParseCertificate(tlsCert.Certificate[0])
	ski, _ := shipcert.SkiFromCertificate(leaf)
	log.Printf("[EEBUS] Eigener Cerbo SKI: %s", ski)

	config, err := eebusapi.NewConfiguration(
		Manufacturer,
		Manufacturer,
		DeviceModel,
		"1000000001",
		spinemodel.DeviceTypeTypeEnergyManagementSystem,
		[]spinemodel.EntityTypeType{spinemodel.EntityTypeTypeCEM},
		4712,
		tlsCert,
		10*time.Minute,
	)
	if err != nil {
		log.Fatalf("Konfigurationsfehler: %v", err)
	}

	srv := service.NewService(config, handler)
	if err := srv.Setup(); err != nil {
		log.Fatalf("Setup-Fehler: %v", err)
	}

	// CEM-Entity abrufen und LoadControl Server Feature registrieren
	localEntity := srv.LocalDevice().EntityForType(spinemodel.EntityTypeTypeCEM)
	if localEntity != nil {
		f := localEntity.GetOrAddFeature(
			spinemodel.FeatureTypeTypeLoadControl,
			spinemodel.RoleTypeServer,
		)
		// Schreibberechtigung für Dimmbefehle freischalten
		f.AddFunctionType(spinemodel.FunctionTypeLoadControlLimitListData, true, true)
		log.Println("[EEBUS] LoadControl Server Feature auf Cerbo registriert.")
	}

	handler.srv = srv
	srv.Start()
	log.Println("[EEBUS] Service lauscht auf Port 4712. Bereit für § 14a Signale.")

	// MQTT-Listener für manuelle SKI-Freigabe (Variante A)
	if mqttClient.IsConnected() {
		mqttClient.Subscribe("eebus/service/register_ski", 0, func(c mqtt.Client, m mqtt.Message) {
			type SkiMsg struct {
				SKI string `json:"ski"`
			}
			var req SkiMsg
			payloadStr := string(m.Payload())

			// Unterstützt reinen Text ("82973a...") oder JSON ({"ski":"82973a..."})
			if err := json.Unmarshal(m.Payload(), &req); err == nil && req.SKI != "" {
				payloadStr = req.SKI
			}

			log.Printf("[EEBUS Freigabe] Registriere Remote-SKI via MQTT: %s", payloadStr)
			srv.RegisterRemoteSKI(payloadStr)

			// Korrektur: handler statt h
			handler.publishJSON("eebus/service/registered_skis", map[string]any{
				"registered_ski": payloadStr,
				"timestamp":      time.Now().Format(time.RFC3339),
			})
		})
		log.Println("[MQTT] Abonniert: eebus/service/register_ski")
	}
	log.Println("[EEBUS] Service lauscht auf Port 4712. Bereit für § 14a Signale.")

	// Heartbeat für Node-RED
	stopHeartbeat := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if mqttClient.IsConnected() {
					payload, _ := json.Marshal(map[string]any{
						"status":    "running",
						"cerbo_ski": ski,
						"port":      4712,
						"timestamp": time.Now().Format(time.RFC3339),
					})
					mqttClient.Publish("eebus/heartbeat", 0, false, payload)
				}
			case <-stopHeartbeat:
				return
			}
		}
	}()

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)
	<-stopChan

	close(stopHeartbeat)
	srv.Shutdown()
	mqttClient.Disconnect(250)
}
