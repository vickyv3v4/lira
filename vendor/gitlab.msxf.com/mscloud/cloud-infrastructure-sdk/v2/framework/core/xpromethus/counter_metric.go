package xpromethus

import (
	"sync"
	"time"
)

type CounterMetric struct {
	requestCounter   SlidingTimeWindowReservoir

	mutex *sync.RWMutex
}

func NewCounterMetric() *CounterMetric {
	return &CounterMetric{
		requestCounter: *NewSlidingTimeWindowCounter(120 * time.Second),
		mutex:          new(sync.RWMutex),
	}
}

func (cm *CounterMetric) Increment() {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	cm.requestCounter.Increment(1)
}

func (cm *CounterMetric) IncrementN(duration int64) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	cm.requestCounter.Increment(duration)
}

func (cm *CounterMetric) GetMetric() *metricData {
	var metricMap = make(metricData)
	var throughput = cm.requestCounter.GetCount()

	metricMap["total"] = cm.requestCounter.GetTotal()
	metricMap["count"] = throughput
	metricMap["throughput"] = throughput / 120

	return &metricMap
}
