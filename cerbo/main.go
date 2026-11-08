package main

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	eebusapi "github.com/enbility/eebus-go/api"
	"github.com/enbility/eebus-go/service"
	shipapi "github.com/enbility/ship-go/api"
	shipcert "github.com/enbility/ship-go/cert"
	spineapi "github.com/enbility/spine-go/api"
	spinemodel "github.com/enbility/spine-go/model"
	"github.com/enbility/spine-go/spine"
)

const (
	CertDir      = "/data/apps/eebus/cert"
	MqttBroker   = "tcp://127.0.0.1:1883"
	ServiceName  = "VictronCerboEEBUS"
	Manufacturer = "VictronCommunity"
	DeviceModel  = "CerboGX-EEBUS"
)

type ServiceHandler struct {
	mqttClient   mqtt.Client
	srv          eebusapi.ServiceInterface
	knownSkis    map[string]bool
	knownSkisMux sync.Mutex
}

func (h *ServiceHandler) publishJSON(topic string, data any) {
	if h.mqttClient == nil || !h.mqttClient.IsConnected() {
		return
	}
	payload, err := json.Marshal(data)
	if err == nil {
		h.mqttClient.Publish(topic, 0, false, payload)
		log.Printf("[MQTT OK] Nachricht an '%s' gesendet.", topic)
	}
}

func (h *ServiceHandler) RemoteSKIConnected(srv eebusapi.ServiceInterface, ski string) {
	h.publishJSON("eebus/status", map[string]any{"connected": true, "remote_ski": ski})
}

func (h *ServiceHandler) RemoteSKIDisconnected(srv eebusapi.ServiceInterface, ski string) {
	// Send a "connected: false" status when the connection drops
	h.publishJSON("eebus/status", map[string]any{"connected": false, "remote_ski": ski})
}

func (h *ServiceHandler) VisibleRemoteServicesUpdated(srv eebusapi.ServiceInterface, entries []shipapi.RemoteService) {
	h.knownSkisMux.Lock()
	defer h.knownSkisMux.Unlock()

	var deviceList []string
	for _, entry := range entries {
		deviceList = append(deviceList, entry.Ski)
		if !h.knownSkis[entry.Ski] {
			h.knownSkis[entry.Ski] = true
			if h.srv != nil {
				h.srv.RegisterRemoteSKI(entry.Ski)
			}
		}
	}

	// NEU: Liste der gefundenen Remote-Services an Node-RED senden!
	h.publishJSON("eebus/discovery", map[string]any{
		"count":   len(entries),
		"devices": deviceList,
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

func (h *ServiceHandler) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {}
func (h *ServiceHandler) ServiceShipIDUpdate(ski string, shipID string) {}

// SPINE Event Handler
func (h *ServiceHandler) HandleEvent(payload spineapi.EventPayload) {
	if payload.Data == nil {
		return
	}

	// 1. NEU: Heartbeat (DeviceDiagnosis) abfangen und an Node-RED senden
	if _, ok := payload.Data.(*spinemodel.DeviceDiagnosisStateDataType); ok {
		h.publishJSON("eebus/heartbeat", map[string]any{
			"status":    "active",
			"cerbo_ski": payload.Ski,
			"timestamp": time.Now().Format(time.RFC3339),
		})
		// log.Printf("[EEBUS] Heartbeat empfangen") // Optional: Auskommentiert, damit der Log nicht zu unübersichtlich wird
		return
	}

	// 2. Leistungs-Limits auswerten (wie bisher)
	if data, ok := payload.Data.(*spinemodel.LoadControlLimitListDataType); ok && data != nil {
		for _, item := range data.LoadControlLimitData {
			if item.Value == nil || item.Value.Number == nil || item.LimitId == nil {
				continue
			}

			// Leistungswert (Watt) errechnen
			val := float64(*item.Value.Number)
			if item.Value.Scale != nil {
				for i := 0; i < int(*item.Value.Scale); i++ {
					val *= 10
				}
				for i := 0; i > int(*item.Value.Scale); i-- {
					val /= 10
				}
			}

			limitId := *item.LimitId

			// Unterscheidung zwischen § 14a (Bezug) und EEG (Einspeisung)
			if limitId == 1 {
				log.Printf("[EEBUS § 14a] Vorgabe Netzbezug empfangen: %.1f W", val)
				h.publishJSON("eebus/grid/limit", map[string]any{
					"active":        val > 0 && val < 25000,
					"power_limit_w": val,
					"source":        "eebus",
					"timestamp":     time.Now().Format(time.RFC3339),
				})
			} else if limitId == 2 {
				log.Printf("[EEBUS EEG] Vorgabe PV-Einspeisung empfangen: %.1f W", val)
				h.publishJSON("eebus/pv/limit", map[string]any{
					"active":        val < 30000,
					"power_limit_w": val,
					"source":        "eebus",
					"timestamp":     time.Now().Format(time.RFC3339),
				})
			}
		}
		return
	}
}

func (h *ServiceHandler) HandleSpineEvent(payload spineapi.EventPayload) {
	h.HandleEvent(payload)
}

func ensureCertificate(certFile, keyFile string) (tls.Certificate, error) {
	if _, err := os.Stat(certFile); err == nil {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}
	tlsCert, err := shipcert.CreateCertificate(ServiceName, Manufacturer, "DE", ServiceName)
	if err != nil {
		return tls.Certificate{}, err
	}
	certOut, _ := os.Create(certFile)
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: tlsCert.Certificate[0]})
	certOut.Close()

	if ecdsaKey, ok := tlsCert.PrivateKey.(*ecdsa.PrivateKey); ok {
		keyBytes, _ := x509.MarshalECPrivateKey(ecdsaKey)
		keyOut, _ := os.Create(keyFile)
		pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
		keyOut.Close()
	}
	return tlsCert, nil
}

func main() {
	log.Println("Starte EEBUS Service für Cerbo GX...")

	mqttOpts := mqtt.NewClientOptions().AddBroker(MqttBroker).SetClientID("cerbo-eebus-bridge")
	mqttOpts.SetAutoReconnect(true)
	mqttClient := mqtt.NewClient(mqttOpts)
	mqttClient.Connect()

	handler := &ServiceHandler{
		mqttClient: mqttClient,
		knownSkis:  make(map[string]bool),
	}
	
	// Auf eingehende MQTT-Nachrichten lauschen (z. B. manuelles Pairing)
	mqttClient.Subscribe("eebus/service/register_ski", 0, func(client mqtt.Client, msg mqtt.Message) {
		var payload struct {
			Ski string `json:"ski"`
		}
		if err := json.Unmarshal(msg.Payload(), &payload); err == nil && payload.Ski != "" {
			log.Printf("[MQTT IN] Manueller Pairing-Befehl fuer SKI erhalten: %s", payload.Ski)
			
			// SKI in der lokalen Liste als "bekannt" markieren
			handler.knownSkisMux.Lock()
			handler.knownSkis[payload.Ski] = true
			handler.knownSkisMux.Unlock()

			// SKI beim EEBUS-Service registrieren, falls dieser schon laeuft
			if handler.srv != nil {
				handler.srv.RegisterRemoteSKI(payload.Ski)
				log.Printf("[EEBUS] SKI %s erfolgreich fuer Pairing freigeschaltet.", payload.Ski)
			}
		} else {
			log.Printf("[MQTT ERROR] Ungueltiges Pairing-Payload: %s", string(msg.Payload()))
		}
	})

	if err := os.MkdirAll(CertDir, 0755); err != nil {
		log.Fatalf("Zertifikatsordner fehlerhaft: %v", err)
	}
	certFile := filepath.Join(CertDir, "cert.crt")
	keyFile := filepath.Join(CertDir, "cert.key")

	tlsCert, _ := ensureCertificate(certFile, keyFile)

	config, _ := eebusapi.NewConfiguration(
		Manufacturer, Manufacturer, DeviceModel, "1000000001",
		spinemodel.DeviceTypeTypeEnergyManagementSystem,
		[]spinemodel.EntityTypeType{spinemodel.EntityTypeTypeCEM},
		4712, tlsCert, 10*time.Minute,
	)

	srv := service.NewService(config, handler)
	srv.Setup()
	spine.Events.Subscribe(handler)

	localEntity := srv.LocalDevice().EntityForType(spinemodel.EntityTypeTypeCEM)
	if localEntity != nil {
		f := localEntity.GetOrAddFeature(spinemodel.FeatureTypeTypeLoadControl, spinemodel.RoleTypeServer)
		f.AddFunctionType(spinemodel.FunctionTypeLoadControlLimitListData, true, true)
		f.AddFunctionType(spinemodel.FunctionTypeLoadControlLimitDescriptionListData, true, false)

		// BEIDE Limits initialisieren (1 = Bezug, 2 = PV)
		limitId1 := spinemodel.LoadControlLimitIdType(1)
		limitId2 := spinemodel.LoadControlLimitIdType(2)
		initialLimits := &spinemodel.LoadControlLimitListDataType{
			LoadControlLimitData: []spinemodel.LoadControlLimitDataType{
				{LimitId: &limitId1, Value: spinemodel.NewScaledNumberType(0)},
				{LimitId: &limitId2, Value: spinemodel.NewScaledNumberType(0)},
			},
		}
		f.SetData(spinemodel.FunctionTypeLoadControlLimitListData, initialLimits)
	}

	handler.srv = srv
	srv.Start()
	log.Println("[EEBUS] Service lauscht auf Port 4712.")

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)
	<-stopChan
	srv.Shutdown()
}