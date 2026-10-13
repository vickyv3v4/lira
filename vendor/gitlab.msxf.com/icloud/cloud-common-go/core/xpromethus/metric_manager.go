package xpromethus

import (
	"sync"
)

var MetricManager = &metricManager{
	metricMap: sync.Map{},
}

type metricManager struct {
	metricMap sync.Map
}

func (m *metricManager) RegisterMetric(method string) *Metric {
	var i, _ = m.metricMap.LoadOrStore(method, NewRequestMetric())
	return i.(*Metric)
}
//
//func (m *metricManager) RegisterDBMetric(dbName string) *DBMetric {
//	var i, _ = m.metricMap.LoadOrStore(dbName, NewDBMetric())
//	return i.(*DBMetric)
//}