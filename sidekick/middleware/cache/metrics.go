package cache

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// cacheEvents counts full-page cache outcomes by label. Registered once on the
// default Prometheus registry (which Caddy's metrics endpoint gathers), so it is
// safe across module reloads/re-provisions.
var cacheEvents = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "wp_cache",
		Name:      "events_total",
		Help:      "FrankenWP full-page cache events by outcome (hit, hit_304, miss, bypass, purge, flush).",
	},
	[]string{"outcome"},
)

func recordEvent(outcome string) {
	cacheEvents.WithLabelValues(outcome).Inc()
}
