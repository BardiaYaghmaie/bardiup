# Operations

## Monitoring

The controller exposes Prometheus metrics on port 8080 (`bardiup-metrics` Service). Enable `metrics.serviceMonitor.enabled` with the Prometheus Operator.

| Metric | Type | Labels |
|---|---|---|
| `bardiup_backup_last_success_timestamp_seconds` | gauge | namespace, backup_config, pvc |
| `bardiup_backup_size_bytes` | gauge | namespace, backup_config, pvc |
| `bardiup_backups_total` | counter | namespace, backup_config, result (`completed`/`failed`) |
| `bardiup_backup_duration_seconds` | histogram | namespace, backup_config |
| `bardiup_restores_total` | counter | namespace, result |
| `bardiup_retention_pruned_total` | counter | namespace, backup_config |
| `bardiup_backupconfig_ready` | gauge | namespace, backup_config |

Standard controller-runtime metrics (reconcile errors, work queue depth, Go runtime) are also exported.

### Alerts

`metrics.prometheusRule.enabled=true` installs three alerts:

- **BardiupBackupStale**: a PVC has had no successful backup for `staleAfterHours` (default 26).
- **BardiupBackupFailed**: a backup failed in the last hour.
- **BardiupBackupConfigNotReady**: a config has been invalid, or its bucket unreachable, for 15 minutes.

The stale-backup alert is the one that matters. It catches every failure mode, including ones that produce no error at all.

## Day-to-day commands

```bash
kubectl get bardiup -A                                        # everything
kubectl get bcfg -A -o wide                                   # health of all schedules
kubectl get backups -l bardiup.io/pvc=data-pg-0               # history of one volume
kubectl annotate bcfg nightly bardiup.io/backup-now="$(date +%s)" --overwrite
kubectl patch bcfg nightly --type merge -p '{"spec":{"suspend":true}}'
kubectl describe backup <name>                                # events and failure reason
kubectl logs job/backup-<name>                                # mover output (failed jobs are kept 24h)
```

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `Ready=False`, `StorageUnavailable` | Wrong bucket, endpoint, region or credentials. The message contains the S3 error. Self-hosted stores usually need `forcePathStyle: true`. |
| `Ready=False`, `InvalidSpec` | Invalid cron expression, time zone, custom retention period or exclude pattern. The message says which. |
| `PVCS` is 0 | The selector matches nothing, or the PVCs aren't `Bound` yet. |
| Backup `Failed`: `PodSecurity` | The namespace enforces `restricted`; set `job.securityContext` ([security.md](security.md)). |
| Backup `Failed`: `VolumeInUse` | `ReadWriteOncePod` volume mounted by a running pod. Use `method: snapshot`. |
| Backup `Failed`: `SnapshotsUnavailable` | The cluster has no VolumeSnapshot API. Use `method: copy` or install the CSI snapshot controller. |
| Backup stuck in `Running` | `kubectl describe job backup-<name>`: usually an unschedulable pod (resources, taints) or a volume that can't attach. `job.activeDeadlineSeconds` (6h by default) bounds it. |
| Backup message mentions "changed during backup" | Files were written during a live `copy`. Use `method: snapshot` for crash-consistent backups, or quiesce the application. |
| `Backup` won't delete | The finalizer couldn't delete the archive (credentials removed, bucket gone). The event explains why. Set `spec.deletionPolicy: Retain` to remove the object and keep the data. |

## Performance and sizing

- **Controller:** one Deployment. It caches BackupConfigs, Backups, Restores, PVCs and only Bardiup's own Jobs. Pods and Secrets are read on demand, never cached cluster-wide, so memory stays flat as the cluster grows.
- **Movers:** one short-lived Job per volume, which exits when it's done. Nothing runs on your nodes between backups.
- **Throughput:** compression is parallel (pgzip) and multipart uploads go up to four parts at a time. Part size scales with the volume, so multi-terabyte volumes stay within S3's 10,000-part limit. Upload buffers take about 80 MiB for volumes up to 140 GiB, about 640 MiB at 1 TiB and about 800 MiB at 2 TiB. For volumes above ~3 TiB, raise `job.resources.limits.memory` from the 1Gi default.
- **Downloads** resume with ranged requests if the connection drops, so large restores survive network blips.

## Consistency

| Method | Consistency | Requirements |
|---|---|---|
| `copy` | Files are copied while the application runs. Fine for most data; databases may need recovery on restore. | Any filesystem volume. |
| `snapshot` | Crash-consistent: the CSI snapshot freezes a point in time and the mover reads a clone of it. The application isn't affected. | CSI driver with snapshot support. |

For databases, combine `snapshot` with the database's own crash-recovery, or back up a dump volume the database writes on a schedule.

## Upgrading

```bash
helm upgrade bardiup oci://ghcr.io/bardiayaghmaie/charts/bardiup --version <new> -n bardiup-system
```

Helm doesn't upgrade CRDs. Apply them first:

```bash
kubectl apply --server-side -f https://github.com/BardiaYaghmaie/bardiup/releases/download/<version>/bardiup.yaml
```

The `v1alpha1` API may change before `v1`. Changes are listed in the [changelog](../CHANGELOG.md).

## Uninstalling

```bash
helm uninstall bardiup -n bardiup-system
```

Backups stay in the bucket. The CRDs and `Backup` objects stay in the cluster. `Backup` objects carry a finalizer, so remove backups **before** uninstalling if you want their data deleted, or strip the finalizers if you want to keep the data and drop the objects:

```bash
kubectl get backups -A -o name | xargs -I{} kubectl patch {} --type merge -p '{"metadata":{"finalizers":[]}}'
```
