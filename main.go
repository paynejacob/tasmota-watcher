package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
	"main/pkg/tasmota_watcher"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var hassHost string
var hassToken string

const (
	entityListResultId = iota + 1
	eventSubscribeResultId
	getStatesResultId
)

func main() {
	var err error
	var conn *websocket.Conn
	entityIPLookup := tasmota_watcher.NewEntityIPLookup()

	// read in configuration
	flag.StringVar(&hassHost, "hass_host", "", "home name of the target home assistant")
	flag.StringVar(&hassToken, "hass_token", "", "home assistant access token")
	flag.Parse()

	logrus.SetLevel(logrus.DebugLevel)

	// open our connection
	conn, _, err = websocket.DefaultDialer.Dial(fmt.Sprintf("wss://%s/api/websocket", hassHost), nil)
	if err != nil {
		logrus.Fatalf("failed to connect to websocket: %s", err.Error())
	}
	defer func() { _ = conn.Close() }()

	// request state refresh on an interval to ensure we never stop trying to restart
	go refreshStateInterval(conn)

	// consume messages
	var data []byte
	var message tasmota_watcher.Message
	for {
		_, data, err = conn.ReadMessage()
		if err != nil {
			logrus.Fatal("failed to read websocket message")
		}

		if json.Unmarshal(data, &message) != nil {
			logrus.Warnf("received non json message: %s", data)
		}

		switch message.Type {

		// hass is ready to talk and we can authenticate
		case "auth_required":
			if tasmota_watcher.SendMessage(conn, "auth", &tasmota_watcher.AuthMessage{AccessToken: hassToken}) != nil {
				logrus.Fatal("failed to write message")
			}
			break

		// we are authenticated and can hydrate our entity ip lookup
		case "auth_ok":
			logrus.Infof("listening for events from [%s]", hassHost)

			if tasmota_watcher.SendMessage(conn, "config/entity_registry/list", &tasmota_watcher.ResultRequest{Id: entityListResultId}) != nil {
				logrus.Fatal("failed to write message")
			}
			break

		// bad token, this is unrecoverable
		case "auth_invalid":
			logrus.Fatal("invalid access token")

		// a state change has occurred
		case "event":
			go processStateChange(&entityIPLookup, data)
			break

		// our entity list is ready and we can hydrate our lookup
		case "result":
			switch message.Id {
			case getStatesResultId:
				ProcessStateList(&entityIPLookup, hassHost, hassToken, data)
				break
			case entityListResultId:
				processEntityList(&entityIPLookup, data)

				// now that our lookup is populated we can start listening to events and get an intial state
				if tasmota_watcher.SendMessage(conn, "subscribe_events", &tasmota_watcher.SubscribeEvents{Id: eventSubscribeResultId, EventType: "state_changed"}) != nil {
					logrus.Fatal("failed to write message")
				}
				break
			}
			break
		}
	}
}

func refreshStateInterval(conn *websocket.Conn) {
	for range time.NewTicker(5 * time.Minute).C {
		if tasmota_watcher.SendMessage(conn, "get_states", &tasmota_watcher.Message{Id: getStatesResultId}) != nil {
			logrus.Error("failed to refresh states")
		}
	}
}

func processStateChange(entityIPLookup *tasmota_watcher.EntityIPLookup, data []byte) {
	var message tasmota_watcher.Event

	// parse the event message
	if json.Unmarshal(data, &message) != nil {
		logrus.Errorf("failed to parse event: %s", data)
		return
	}

	if isIPSensorEntity(message.Event.Data.EntityId) {
		if message.Event.Data.NewState.State == "unavailable" {
			return
		}

		entityIPLookup.UpdateIP(message.Event.Data.EntityId, message.Event.Data.NewState.State)
	} else if isSwitchEntity(message.Event.Data.EntityId) {
		if message.Event.Data.NewState.State == "unavailable" {
			entityIPLookup.RestartEntityDevice(message.Event.Data.EntityId)
		}
	}
}

func processEntityList(entityIPLookup *tasmota_watcher.EntityIPLookup, data []byte) {
	var message tasmota_watcher.EntityListResult

	if json.Unmarshal(data, &message) != nil {
		logrus.Errorf("failed to parse entity list: %s", data)
		return
	}

	for i := 0; i < len(message.Result); i++ {
		if isIPSensorEntity(message.Result[i].EntityId) {
			entityIPLookup.BindDeviceSensor(message.Result[i].DeviceId, message.Result[i].EntityId)
		} else if isSwitchEntity(message.Result[i].EntityId) {
			entityIPLookup.BindDeviceEntity(message.Result[i].DeviceId, message.Result[i].EntityId)
		}
	}
}

func ProcessStateList(entityIPLookup *tasmota_watcher.EntityIPLookup, hassHost, hassToken string, data []byte) {
	var message tasmota_watcher.StateListResult

	if json.Unmarshal(data, &message) != nil {
		logrus.Errorf("failed to parse state list: %s", data)
		return
	}

	switchEntites := map[string]string{} // entity id to state

	for i := 0; i < len(message.Result); i++ {

		if isIPSensorEntity(message.Result[i].EntityId) {
			fakeEvent := tasmota_watcher.Event{}
			fakeEvent.Event.Data.EntityId = message.Result[i].EntityId
			fakeEvent.Event.Data.NewState.State = message.Result[i].State

			if fakeEvent.Event.Data.NewState.State == "unavailable" {
				var err error
				fakeEvent.Event.Data.NewState.State, err = getLastKnownIP(message.Result[i].EntityId, hassHost, hassToken)
				if err != nil || fakeEvent.Event.Data.NewState.State == "" {
					logrus.Warningf("unable to find ip address for sensor [%s]", message.Result[i].EntityId)
					continue
				}
			}

			fakeData, _ := json.Marshal(fakeEvent)

			processStateChange(entityIPLookup, fakeData)
		} else if isSwitchEntity(message.Result[i].EntityId) {
			switchEntites[message.Result[i].EntityId] = message.Result[i].State
		}
	}

	for entityId, state := range switchEntites {
		fakeEvent := tasmota_watcher.Event{}
		fakeEvent.Event.Data.EntityId = entityId
		fakeEvent.Event.Data.NewState.State = state

		fakeData, _ := json.Marshal(fakeEvent)

		go processStateChange(entityIPLookup, fakeData)
	}

}

func getLastKnownIP(sensorId, hassHost, hassToken string) (string, error) {
	u := &url.URL{
		Scheme: "https",
		Host:   hassHost,
		Path:   fmt.Sprintf("/api/history/period/%s", (time.Now().UTC().AddDate(0, -3, 0)).Format(time.RFC3339)),
	}

	q := u.Query()
	q.Set("filter_entity_id", sensorId)
	q.Set("minimal_response", "")
	q.Set("end_time", time.Now().UTC().Format(time.RFC3339))
	u.RawQuery = q.Encode()

	req := &http.Request{
		Method: http.MethodGet,
		URL:    u,
		Header: map[string][]string{"Authorization": {fmt.Sprintf("Bearer %s", hassToken)}},
	}
	client := http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	var body []interface{}

	err = json.NewDecoder(resp.Body).Decode(&body)
	if err != nil {
		return "", err
	}

	for _, entityResult := range body {
		if states, ok := entityResult.([]interface{}); ok {
			for _, state := range states {
				entityState, _ := state.(map[string]interface{})
				if val, ok := entityState["state"].(string); ok && val != "unavailable" {
					return val, nil
				}
			}
		}
	}

	return "", nil
}

func isIPSensorEntity(entityId string) bool {
	return strings.HasPrefix(entityId, "sensor.") && strings.Contains(entityId, "_ip")
}

func isSwitchEntity(entityId string) bool {
	return strings.HasPrefix(entityId, "light.") || strings.HasPrefix(entityId, "switch.")
}
