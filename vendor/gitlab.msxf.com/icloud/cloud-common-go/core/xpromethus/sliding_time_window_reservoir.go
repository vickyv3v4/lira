package xpromethus

import (
	"time"
)

type SlidingTimeWindowReservoir struct {
	total  float64
	head   *node
	tail   *node
	period time.Duration
}

type node struct {
	next  *node
	count int
	values []int64
	ts    int64
}

func NewSlidingTimeWindowCounter(t time.Duration) *SlidingTimeWindowReservoir {
	r := &SlidingTimeWindowReservoir{
		total:  0,
		head:   nil,
		tail:   nil,
		period: t,
	}

	return r
}

func (a *SlidingTimeWindowReservoir) Increment(duration int64) {
	a.total++

	var now = time.Now().Unix()

	if a.head != nil && a.tail.ts == now {
		a.tail.count++
		a.tail.values = append(a.tail.values, duration)
	} else {
		var newNode = &node{
			next:  nil,
			count: 1,
			ts:    now,
			values: []int64{duration},
		}

		if a.head == nil {
			a.head = newNode
			a.tail = newNode
		} else {
			a.tail.next = newNode
			a.tail = newNode
		}
	}
}

func (a *SlidingTimeWindowReservoir) GetMetrics() []int64 {
	a.periodicClear()

	var snapshot []int64

	var p = a.head
	for p != nil {
		snapshot = append(snapshot, p.values...)
		p = p.next
	}

	return snapshot
}

func (a *SlidingTimeWindowReservoir) GetCount() float64 {
	a.periodicClear()

	var count float64 = 0

	var p = a.head
	for p != nil {
		count= count + float64(p.count)
		p = p.next
	}

	return count
}

func (a *SlidingTimeWindowReservoir) GetTotal() float64 {
	a.periodicClear()

	return a.total
}

func (a *SlidingTimeWindowReservoir) periodicClear() {
	nowTs := time.Now().Unix()
	deadline := uint64(nowTs - int64(a.period.Seconds()))
	a.moveOutExpired(deadline)
}

func (a *SlidingTimeWindowReservoir) moveOutExpired(deadline uint64) {
	if a.head == nil {
		a.tail = nil
	} else if uint64(a.head.ts) > deadline {
		return
	} else {
		a.head = a.head.next
		a.moveOutExpired(deadline)
	}
}
