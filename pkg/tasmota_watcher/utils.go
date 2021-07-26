package tasmota_watcher

import "strings"

func IsIPSensorEntity(entityId string) bool {
	return strings.HasPrefix(entityId, "sensor.") && strings.Contains(entityId, "_ip")
}

func IsSwitchEntity(entityId string) bool {
	return strings.HasPrefix(entityId, "light.") || strings.HasPrefix(entityId, "switch.")
}
