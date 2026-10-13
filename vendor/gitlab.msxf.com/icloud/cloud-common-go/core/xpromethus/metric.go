package xpromethus

import (
	"github.com/HdrHistogram/hdrhistogram-go"
	"sync"
	"time"
)

/***
	Author : wei.peng03
	golang 监控包
*/

type metricData map[string]float64

type Metric struct {
	requestErrorCounter SlidingTimeWindowReservoir
	requestPanicCounter SlidingTimeWindowReservoir
	requestTotalCounter SlidingTimeWindowReservoir
	requestHistogram    *hdrhistogram.Histogram

	mutex *sync.RWMutex
}

func NewRequestMetric() *Metric {
	return &Metric{
		requestErrorCounter: *NewSlidingTimeWindowCounter(120 * time.Second),
		requestPanicCounter: *NewSlidingTimeWindowCounter(120 * time.Second),
		requestTotalCounter: *NewSlidingTimeWindowCounter(120 * time.Second),
		mutex:               new(sync.RWMutex),
	}
}

func (r *Metric) Start() *MetricContext {
	return NewMetricContext(r)
}

func (r *Metric) appendRequest(elapsed time.Duration, t resultType) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if t == requestError {
		r.requestErrorCounter.Increment(elapsed.Milliseconds())
	} else if t == requestPanic {
		r.requestPanicCounter.Increment(elapsed.Milliseconds())
	}

	r.requestTotalCounter.Increment(elapsed.Milliseconds())
}

func (r *Metric) Snapshot() *Metric {
	var snapshot = hdrhistogram.New(0, time.Minute.Milliseconds(), 1)

	for _, v := range r.requestTotalCounter.GetMetrics() {
		_ = snapshot.RecordValue(v)
	}

	return &Metric{
		requestErrorCounter: r.requestErrorCounter,
		requestPanicCounter: r.requestPanicCounter,
		requestTotalCounter: r.requestTotalCounter,
		requestHistogram:    hdrhistogram.Import(snapshot.Export()),
	}
}

func (r *Metric) GetMetric() *metricData {
	var snapshot = r.Snapshot()
	var throughput = snapshot.requestTotalCounter.GetCount()
	var metricMap = make(metricData)

	metricMap["totalCount"] = throughput
	metricMap["throughput"] = throughput / 120
	metricMap["averageTimeInMs"] = snapshot.requestHistogram.Mean()
	metricMap["maxTimeInMs"] = float64(snapshot.requestHistogram.Max())
	metricMap["50p"] = float64(snapshot.requestHistogram.ValueAtQuantile(50))
	metricMap["90p"] = float64(snapshot.requestHistogram.ValueAtQuantile(90))
	metricMap["95p"] = float64(snapshot.requestHistogram.ValueAtQuantile(95))
	metricMap["99p"] = float64(snapshot.requestHistogram.ValueAtQuantile(99))
	metricMap["errorCount"] = snapshot.requestErrorCounter.GetCount()
	metricMap["panicCount"] = snapshot.requestPanicCounter.GetCount()

	if snapshot.requestTotalCounter.GetCount() != 0 {
		metricMap["successRate"] = 1 - ((snapshot.requestPanicCounter.GetCount() + snapshot.requestErrorCounter.GetCount()) /
			snapshot.requestTotalCounter.GetCount())
	} else {
		metricMap["successRate"] = 1
	}

	return &metricMap
}

type MetricContext struct {
	startTime time.Time
	metric    *Metric
}

func NewMetricContext(requestMetric *Metric) *MetricContext {
	return &MetricContext{
		startTime: time.Now(),
		metric:    requestMetric,
	}
}

func (c *MetricContext) Failure(isPanic bool) {
	var endTime = time.Now()

	if isPanic {
		c.metric.appendRequest(endTime.Sub(c.startTime), requestPanic)
	} else {
		c.metric.appendRequest(endTime.Sub(c.startTime), requestError)
	}
}

func (c *MetricContext) Success() {
	var endTime = time.Now()
	c.metric.appendRequest(endTime.Sub(c.startTime), requestSuccess)
}
