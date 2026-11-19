package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	backupsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bardiup_backups_total",
		Help: "Backups finished, by result (completed or failed).",
	}, []string{"namespace", "backup_config", "result"})

	backupDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "bardiup_backup_duration_seconds",
		Help:    "Duration of completed backups.",
		Buckets: prometheus.ExponentialBuckets(5, 2, 13),
	}, []string{"namespace", "backup_config"})

	backupSize = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "bardiup_backup_size_bytes",
		Help: "Compressed size of the newest completed backup of a PVC.",
	}, []string{"namespace", "backup_config", "pvc"})

	lastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "bardiup_backup_last_success_timestamp_seconds",
		Help: "Completion time of the newest completed backup of a PVC.",
	}, []string{"namespace", "backup_config", "pvc"})

	restoresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bardiup_restores_total",
		Help: "Restores finished, by result (completed or failed).",
	}, []string{"namespace", "result"})

	prunedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bardiup_retention_pruned_total",
		Help: "Backups deleted by retention.",
	}, []string{"namespace", "backup_config"})

	configReady = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "bardiup_backupconfig_ready",
		Help: "1 when the BackupConfig is valid and its storage is reachable.",
	}, []string{"namespace", "backup_config"})
)

func init() {
	metrics.Registry.MustRegister(backupsTotal, backupDuration, backupSize, lastSuccess, restoresTotal, prunedTotal, configReady)
}

func forgetConfigMetrics(namespace, name string) {
	l := prometheus.Labels{"namespace": namespace, "backup_config": name}
	backupSize.DeletePartialMatch(l)
	lastSuccess.DeletePartialMatch(l)
	configReady.DeletePartialMatch(l)
}
