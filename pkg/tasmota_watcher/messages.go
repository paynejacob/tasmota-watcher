package tasmota_watcher

import "github.com/gorilla/websocket"

type IMessage interface {
	SetType(string)
}

type Message struct {
	Id   int    `json:"id,omitempty"`
	Type string `json:"type"`
}

func (m *Message) SetType(t string) {
	m.Type = t
}

func SendMessage(c *websocket.Conn, t string, message IMessage) error {
	message.SetType(t)
	return c.WriteJSON(message)
}

type AuthMessage struct {
	Message     `json:""`
	AccessToken string `json:"access_token"`
}

type SubscribeEvents struct {
	Message
	Id        int    `json:"id"`
	EventType string `json:"event_type"`
}

type Event struct {
	Event struct {
		Data struct {
			EntityId string `json:"entity_id"`
			NewState struct {
				State string `json:"state"`
			} `json:"new_state"`
		} `json:"data"`
	} `json:"event"`
}

type ResultRequest struct {
	Message
	Id int `json:"id"`
}

type ResultResponse struct {
	Message

	Id      int  `json:"id"`
	Success bool `json:"success"`
}

type EntityListResult struct {
	ResultResponse

	Result []struct {
		DeviceId string `json:"device_id"`
		EntityId string `json:"entity_id"`
	} `json:"result"`
}

type StateListResult struct {
	ResultResponse

	Result []struct {
		EntityId string `json:"entity_id"`
		State    string `json:"state"`
	} `json:"result"`
}
