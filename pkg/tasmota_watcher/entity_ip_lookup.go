package tasmota_watcher

import (
	"github.com/sirupsen/logrus"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type EntityIPLookup struct {
	mu sync.RWMutex

	deviceIPLookup       map[string]string // device to ip
	iPSensorDeviceLookup map[string]string // ip sensor to device
	entityDeviceLookup   map[string]string // entity to device

	deviceRestartLockout map[string]time.Time
}

func NewEntityIPLookup() EntityIPLookup {
	return EntityIPLookup{
		mu:                   sync.RWMutex{},
		deviceIPLookup:       map[string]string{},
		iPSensorDeviceLookup: map[string]string{},
		entityDeviceLookup:   map[string]string{},
	}
}

func (r *EntityIPLookup) UpdateIP(sensorId, ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	deviceId, found := r.iPSensorDeviceLookup[sensorId]
	if !found {
		return
	}

	r.deviceIPLookup[deviceId] = ip
}

func (r *EntityIPLookup) BindDeviceSensor(deviceId, sensorId string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.iPSensorDeviceLookup[sensorId] = deviceId
}

func (r *EntityIPLookup) BindDeviceEntity(deviceId, entityId string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.entityDeviceLookup[entityId] = deviceId
}

func (r *EntityIPLookup) RestartEntityDevice(entityId string) {
	var deviceId string
	var deviceIP string
	var found bool
	var waitTimer *time.Timer

	r.mu.RLock()

	// get the associated device
	deviceId, found = r.entityDeviceLookup[entityId]
	if !found {
		logrus.Errorf("cannot determine device id for entity [%s]", entityId)
		return
	}

	// get the device ip
	deviceIP, found = r.deviceIPLookup[deviceId]
	if !found {
		logrus.Errorf("cannot determine device ip for entity [%s]", entityId)
		return
	}

	// check if there is a lockout on the device
	if time.Now().Before(r.deviceRestartLockout[deviceId]) {
		waitTimer = time.NewTimer(r.deviceRestartLockout[deviceId].Sub(time.Now()))
	}

	r.mu.RUnlock()

	// wait for the lockout to expire
	if waitTimer != nil {
		<-waitTimer.C
	}

	u := url.URL{
		Scheme: "http",
		Host:   deviceIP,
		Path:   "cm",
	}

	q := u.Query()
	q.Set("cmnd", "Restart 1")
	u.RawQuery = q.Encode()

	// request a restart
	resp, err := http.Get(u.String())
	if err != nil || resp.StatusCode != 200 {
		logrus.Errorf("failed to restart device for entity [%s]: %s", entityId, err.Error())
		return
	}

	logrus.Infof("device successfully restarted for entity [%s]", entityId)

	// lockout the device for 5m
	r.mu.Lock()
	r.deviceRestartLockout[deviceId] = time.Now().Add(5 * time.Minute)
	r.mu.Unlock()
}
