// Package metrics owns Prometheus metric registration. Names match the
// TS service so existing dashboards and alerts continue to work.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
)

var (
	ReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reconcile_total",
			Help: "Reconcile attempts grouped by trigger and status.",
		},
		[]string{"trigger", "status"},
	)
	ReconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "reconcile_duration_seconds",
			Help:    "Reconcile wall-clock duration.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 120},
		},
		[]string{"trigger"},
	)
	ApplierTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "applier_total",
			Help: "Applier mutations grouped by resource, action and status.",
		},
		[]string{"resource", "action", "status"},
	)
	RateLimitRemaining = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "rate_limit_remaining",
			Help: "GitHub API rate limit remaining per pool.",
		},
		[]string{"pool"},
	)
	QueueSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "queue_size",
			Help: "Rate-limiter queue depth grouped by priority+pool.",
		},
		[]string{"priority"},
	)
	APICallsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "api_calls_total",
			Help: "GitHub API calls grouped by method, endpoint and status.",
		},
		[]string{"method", "endpoint", "status"},
	)
	WebhookEventsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "webhook_events_total",
			Help: "Inbound webhook events grouped by event and action.",
		},
		[]string{"event", "action"},
	)
	PRCheckTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pr_check_total",
			Help: "PR check runs grouped by result.",
		},
		[]string{"result"},
	)
	PolicyViolationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "policy_violations_total",
			Help: "Admin policy violations grouped by severity and field.",
		},
		[]string{"severity", "field"},
	)
)

// Register adds all metrics to the given registry. Call once at startup.
// Using a dedicated registry (rather than prometheus.DefaultRegisterer)
// keeps the test surface clean and lets multiple test cases register
// without panicking on duplicates.
func Register(r prometheus.Registerer) {
	r.MustRegister(
		ReconcileTotal,
		ReconcileDuration,
		ApplierTotal,
		RateLimitRemaining,
		QueueSize,
		APICallsTotal,
		WebhookEventsTotal,
		PRCheckTotal,
		PolicyViolationsTotal,
	)
}

// Handler exposes /metrics for the given gatherer.
func Handler(g prometheus.Gatherer) http.Handler {
	return promhttp.HandlerFor(g, promhttp.HandlerOpts{})
}
