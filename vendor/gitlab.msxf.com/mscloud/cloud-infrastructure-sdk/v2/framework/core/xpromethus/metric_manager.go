package xpromethus

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Label = map[string]string

const labelSep = "?tag:"

var MetricManager = &metricManager{
	metricMap: sync.Map{},
}

type metricManager struct {
	metricMap sync.Map
}

func (m *metricManager) RegisterMetric(method string, labels ...Label) *Metric {
	var i, _ = m.metricMap.LoadOrStore(mergeName(method, labels...), NewRequestMetric())
	return i.(*Metric)
}

func (m *metricManager) RegisterCounter(method string, labels ...Label) *CounterMetric {
	var i, _ = m.metricMap.LoadOrStore(mergeName(method, labels...), NewCounterMetric())
	return i.(*CounterMetric)
}

//RegisterGaugeMetric add gauge metric
func (m *metricManager) RegisterGaugeMetric(method string, labels ...Label) *GaugeMetric {
	var i, _ = m.metricMap.LoadOrStore(mergeName(method, labels...), NewGaugeMetric())
	return i.(*GaugeMetric)
}

func mergeName(method string, labels ...Label) string {
	labelMap := make(map[string]string)
	for _, l := range labels {
		for k, v := range l {
			labelMap[k] = v
		}
	}
	labelKeys := make([]string, 0, len(labelMap))
	for k := range labelMap {
		labelKeys = append(labelKeys, k)
	}
	sort.Strings(labelKeys)

	labelStr := ""
	for _, v := range labelKeys {
		labelStr += fmt.Sprintf("&%s=%s", v, labelMap[v])
	}
	sepLabel := strings.TrimLeft(labelStr, "&")

	if len(sepLabel) > 0 {
		method += labelSep + sepLabel
	}
	return method
}

//
//func (m *metricManager) RegisterDBMetric(dbName string) *DBMetric {
//	var i, _ = m.metricMap.LoadOrStore(dbName, NewDBMetric())
//	return i.(*DBMetric)
//}
