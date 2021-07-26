package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
	"main/pkg/tasmota_watcher"
	"net"
	"time"
)

var hassHost string
var hassToken string
var debug bool

func main() {
	var err error
	var conn *websocket.Conn
	entityIPLookup := tasmota_watcher.NewEntityIPLookup()

	// read in configuration
	flag.StringVar(&hassHost, "hass_host", "", "home name of the target home assistant")
	flag.StringVar(&hassToken, "hass_token", "", "home assistant access token")
	flag.BoolVar(&debug, "debug", false, "enable debug logs")
	flag.Parse()

	if debug {
		logrus.SetLevel(logrus.DebugLevel)
	}

	_, err = newConnection(hassHost, hassToken)
	if err != nil {
		logrus.Fatalf("failed to establish connection: %s", err.Error())
	}

	// Start the main loop
	for {
		conn, err = newConnection(hassHost, hassToken)
		if err != nil {
			logrus.Error("failed to connect to home assistant, retrying...")
			logrus.Debugf("connection error: %s", err.Error())
			<- time.NewTimer(60 * time.Second).C
			continue
		}

		logrus.Infof("connection to %s established", hassHost)

		logrus.Debug("initializing lookup tables")
		err = entityIPLookup.InitializeLookups(conn)
		if err != nil {
			logrus.Debugf("failed to initialized entity lookups: %s", err.Error())
			continue
		}

		logrus.Debug("requesting initial entity state")
		err = tasmota_watcher.SendMessage(conn, "get_states", &tasmota_watcher.Message{Id: tasmota_watcher.GetStatesResultId})
		if err != nil {
			logrus.Debugf("failed to refresh states: %s", err.Error())
			continue
		}

		logrus.Debug("watching for state changes")
		err = tasmota_watcher.SendMessage(conn, "subscribe_events", &tasmota_watcher.SubscribeEvents{Id: tasmota_watcher.EventSubscribeResultId, EventType: "state_changed"})
		if err != nil {
			logrus.Debugf("failed to subscribe to events: %s", err.Error())
			continue
		}

		var data []byte
		var message tasmota_watcher.ResultResponse
		for {
			_, data, err = conn.ReadMessage()
			if err != nil {
				logrus.Debugf("failed to read message: %s", err.Error())
				break
			}

			if json.Unmarshal(data, &message) != nil {
				logrus.Debugf("received non json message: %s", data)
				break
			}

			switch message.Type {
			case "event":
				go processStateChange(&entityIPLookup, data)
				break
			case "result":
				switch message.Id {
				case tasmota_watcher.EventSubscribeResultId:
					if !message.Success {
						logrus.Debugf("failed to subscribe to event updates: %s", message.Error)
					}
				case tasmota_watcher.GetStatesResultId:
					ProcessStateList(&entityIPLookup, data)
					break
				}
			}

		}

		logrus.Warning("connection lost")
	}
}

func authenticateConnection(conn *websocket.Conn, hassToken string) error {
	var err error
	var message tasmota_watcher.Message

	err = tasmota_watcher.SendMessage(conn, "auth", &tasmota_watcher.AuthMessage{AccessToken: hassToken})
	if err != nil {
		return err
	}

	for {
		err = conn.ReadJSON(&message)
		if err != nil {
			logrus.Fatal("failed to read websocket message")
		}

		if message.Type == "auth_invalid" {
			return errors.New("invalid home assistant token")
		}

		if message.Type == "auth_ok" {
			return nil
		}
	}
}

func newConnection(hassHost, hassToken string) (conn *websocket.Conn, err error) {
	conn, _, err = websocket.DefaultDialer.Dial(fmt.Sprintf("wss://%s/api/websocket", hassHost), nil)
	if err != nil {
		return
	}

	err = authenticateConnection(conn, hassToken)

	return
}

func processStateChange(entityIPLookup *tasmota_watcher.EntityIPLookup, data []byte) {
	var message tasmota_watcher.Event

	// parse the event message
	if json.Unmarshal(data, &message) != nil {
		logrus.Errorf("failed to parse event: %s", data)
		return
	}

	// an ip address was updated
	if tasmota_watcher.IsIPSensorEntity(message.Event.Data.EntityId) {
		// if it is not a valid ip address we don't want to store it
		if net.ParseIP(message.Event.Data.NewState.State) != nil {
			entityIPLookup.UpdateIP(message.Event.Data.EntityId, message.Event.Data.NewState.State)
		}
	// an entity state was updated
	} else if tasmota_watcher.IsSwitchEntity(message.Event.Data.EntityId) {
		// if the entity has become unavailable we need to restart it
		if message.Event.Data.NewState.State == "unavailable" {
			entityIPLookup.RestartEntityDevice(message.Event.Data.EntityId)
		}
	}
}

func ProcessStateList(entityIPLookup *tasmota_watcher.EntityIPLookup, data []byte) {
	var message tasmota_watcher.StateListResult

	if json.Unmarshal(data, &message) != nil {
		logrus.Errorf("failed to parse state list: %s", data)
		return
	}

	for i := 0; i < len(message.Result); i++ {
		if tasmota_watcher.IsIPSensorEntity(message.Result[i].EntityId) {
			if net.ParseIP(message.Result[i].State) != nil {
				entityIPLookup.UpdateIP(message.Result[i].EntityId, message.Result[i].State)
			}

		} else if tasmota_watcher.IsSwitchEntity(message.Result[i].EntityId) {
			if message.Result[i].State == "unavailable" {
				// defer switch entities incase their ip entry is new
				entityId := message.Result[i].EntityId
				defer func() {
					go entityIPLookup.RestartEntityDevice(entityId)
				}()
			}
		}
	}
}
