package db

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// MaxGauge mimics Micrometer's *_max series: the largest value observed in the
// last 1-2 minutes, then it decays back to 0. The dashboards use
// app_db_op_seconds_max to show the slowest operation during a failover.
type MaxGauge struct {
	desc    *prometheus.Desc
	values  []string
	window  time.Duration
	mu      sync.Mutex
	cur     map[string]float64
	prev    map[string]float64
	rotated time.Time
}

func NewMaxGauge(name, help, label string, values ...string) *MaxGauge {
	return &MaxGauge{
		desc:    prometheus.NewDesc(name, help, []string{label}, nil),
		values:  values,
		window:  time.Minute,
		cur:     map[string]float64{},
		prev:    map[string]float64{},
		rotated: time.Now(),
	}
}

func (m *MaxGauge) Observe(labelValue string, v float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotate()
	if v > m.cur[labelValue] {
		m.cur[labelValue] = v
	}
}

func (m *MaxGauge) rotate() {
	elapsed := time.Since(m.rotated)
	if elapsed < m.window {
		return
	}
	if elapsed < 2*m.window {
		m.prev = m.cur
	} else {
		m.prev = map[string]float64{}
	}
	m.cur = map[string]float64{}
	m.rotated = time.Now()
}

func (m *MaxGauge) Describe(ch chan<- *prometheus.Desc) { ch <- m.desc }

func (m *MaxGauge) Collect(ch chan<- prometheus.Metric) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotate()
	for _, lv := range m.values {
		ch <- prometheus.MustNewConstMetric(m.desc, prometheus.GaugeValue, max(m.cur[lv], m.prev[lv]), lv)
	}
}
