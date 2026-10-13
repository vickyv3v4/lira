package xpromethus

import (
	"sync"
)

type GaugeMetric struct {
	valfloat64   float64
	mutex *sync.RWMutex
}

func NewGaugeMetric() *GaugeMetric {
	return &GaugeMetric{
		mutex:          new(sync.RWMutex),
	}
}

func (cm *GaugeMetric) Update(val float64) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
    cm.valfloat64 = val
}

func (cm *GaugeMetric) GetMetric() *metricData {
	var metricMap = make(metricData)
	metricMap["count"] = cm.valfloat64
	return &metricMap
}

