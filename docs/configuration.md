# Configuration reference

Bardiup has three namespaced resources in the `bardiup.io/v1alpha1` API group:

| Kind | Short name | Purpose |
|---|---|---|
| `BackupConfig` | `bcfg` | Schedule, PVC selection, storage and retention. Creates `Backup`s. |
| `Backup` | `bk` | One archive of one PVC. Usually created by a `BackupConfig`, can be created by hand. |
| `Restore` | `rst` | Extracts a backup into a new or existing PVC. |

`kubectl get bardiup` lists all three. `kubectl explain backupconfig.spec --recursive` shows the full schema from the cluster.

## BackupConfig

```yaml
apiVersion: bardiup.io/v1alpha1
kind: BackupConfig
metadata:
  name: nightly            # at most 63 characters
  namespace: shop
spec:
  schedule: "0 2 * * *"    # 5-field cron, or @daily, @hourly, @every 6h
  timeZone: Europe/Berlin  # schedule and retention calendar; default UTC
  suspend: false           # stop scheduled runs (manual triggers still work)

  pvcSelector:             # empty selector = every PVC in the namespace
    matchLabels: {app: postgres}
    matchExpressions:
      - {key: tier, operator: In, values: [db]}
    names: [data-postgres-0]   # optional: restrict to these names

  storage:
    s3:
      bucket: backups
      region: eu-central-1               # default us-east-1
      endpoint: https://s3.example.com   # omit for AWS S3
      forcePathStyle: true               # most self-hosted stores need this
      prefix: prod                       # objects go to <prefix>/<namespace>/<pvc>/
      credentialsSecretRef: {name: s3-credentials}  # omit to use IRSA / Pod Identity
      serverSideEncryption: aws:kms      # or AES256
      kmsKeyID: arn:aws:kms:...
      insecureSkipTLSVerify: false

  retention:               # see docs/retention.md
    keepLast: 3
    daily: 7
    weekly: 4
    monthly: 12
    yearly: 2
    custom: [{every: 90d, keep: 8}]

  method: copy             # copy (default) or snapshot
  snapshot:
    volumeSnapshotClassName: csi-snapclass   # default class if omitted

  excludes: ["lost+found", "cache", "*.tmp"]  # path.Match globs, relative to the volume root
  failedBackupsHistoryLimit: 3

  job:                     # data mover Job settings
    resources:
      requests: {cpu: 500m, memory: 256Mi}
      limits: {memory: 1Gi}
    activeDeadlineSeconds: 21600   # 6h
    backoffLimit: 2
    serviceAccountName: backup-irsa
    nodeSelector: {pool: general}
    tolerations: []
    securityContext: {}    # replaces the default; see docs/security.md
```

### Credentials secret

The secret lives in the same namespace as the `BackupConfig`:

```bash
kubectl -n shop create secret generic s3-credentials \
  --from-literal=AWS_ACCESS_KEY_ID=... \
  --from-literal=AWS_SECRET_ACCESS_KEY=...
  # optional: --from-literal=AWS_SESSION_TOKEN=...
```

Without `credentialsSecretRef`, both the controller and the mover pods use the default AWS credential chain. On EKS, use IRSA or Pod Identity: annotate the controller's service account through the chart value `serviceAccount.annotations`, and set `job.serviceAccountName` to a service account with bucket access.

### Which PVCs are selected

A PVC is backed up when it matches `pvcSelector`, is `Bound`, and isn't being deleted. Temporary PVCs Bardiup creates for snapshot backups are always excluded. Block-mode volumes aren't supported.

### Triggering a backup now

```bash
kubectl annotate backupconfig nightly bardiup.io/backup-now="$(date +%s)" --overwrite
```

Any new value of the annotation starts one backup per selected PVC. A PVC that still has a backup in progress is skipped, both for manual and scheduled runs.

### Status

```console
$ kubectl get bcfg -o wide
NAME      SCHEDULE    SUSPEND   PVCS   BACKUPS   READY   LAST SUCCESS   NEXT                   AGE
nightly   0 2 * * *   false     3      21        True    7h             2026-11-20T01:00:00Z   40d
```

Conditions:

- `Ready`: the spec is valid and the bucket is reachable with the configured credentials. Reasons: `InvalidSpec`, `StorageUnavailable`.
- `LastBackupSucceeded`: whether the most recently finished backup completed.

Missed runs (for example while the controller was down) collapse into a single run when it comes back.

## Backup

Backups created by a `BackupConfig` copy its storage, method, excludes and job settings. You can also create one directly for a one-off backup. One-off backups are never touched by retention:

```yaml
apiVersion: bardiup.io/v1alpha1
kind: Backup
metadata: {name: before-migration, namespace: shop}
spec:
  pvcName: data-postgres-0
  storage:
    s3: {bucket: backups, prefix: manual, credentialsSecretRef: {name: s3-credentials}}
  deletionPolicy: Retain   # keep the archive even if this object is deleted
```

Phases: `Pending` → (`Snapshotting`) → `Running` → `Completed` | `Failed`.

| Status field | Meaning |
|---|---|
| `objectKey` | Archive key in the bucket. The manifest is at `<objectKey>.json`. |
| `size`, `sizeBytes` | Compressed archive size. |
| `sha256` | Checksum of the archive, verified on restore. |
| `files` | Number of entries archived. |
| `source` | Storage class, access modes and capacity of the source PVC. These are the defaults for restores into a new PVC. |
| `retainedBy` | Retention rules currently keeping this backup. |
| `message` | Progress, warnings (for example files that changed during a live copy) or the failure reason. |

`spec.pvcName` and `spec.storage` are immutable. `deletionPolicy` can be changed at any time.

## Restore

```yaml
apiVersion: bardiup.io/v1alpha1
kind: Restore
metadata: {name: rollback, namespace: shop}
spec:
  backupName: nightly-data-postgres-0-20261119-020000
  # or, without a Backup object (disaster recovery):
  # source:
  #   objectKey: prod/shop/data-postgres-0/nightly-data-postgres-0-20261119-020000.tar.gz
  #   storage: {s3: {bucket: backups, credentialsSecretRef: {name: s3-credentials}}}
  target:
    pvcName: data-postgres-0-restored  # created if missing
    storageClassName: fast             # default: source PVC's class
    size: 20Gi                         # default: source PVC's capacity (required with source)
    accessModes: [ReadWriteOnce]       # default: source PVC's modes
    cleanTarget: false                 # true: delete existing content first
  job: {}                              # same options as BackupConfig.spec.job
```

Exactly one of `backupName` or `source` must be set. See [restore.md](restore.md) for workflows.

Phases: `Pending` (waiting for the backup to complete) → `Running` → `Completed` | `Failed`.

## Helm values

| Value | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/bardiayaghmaie/bardiup` | Controller and mover image. |
| `image.tag` | chart appVersion | |
| `image.pullPolicy` | `IfNotPresent` | Also used for mover pods. |
| `replicaCount` | `1` | Use 2 for faster failover; only the leader works. |
| `logLevel` | `info` | `info`, `debug` or a number. |
| `resources` | 10m / 48Mi request, 256Mi limit | Controller resources. |
| `serviceAccount.annotations` | `{}` | For example an IRSA role. |
| `aggregateToDefaultRoles` | `true` | Grant the built-in `admin`/`edit`/`view` roles access to Bardiup resources. |
| `metrics.enabled` | `true` | Expose Prometheus metrics on a Service. |
| `metrics.port` | `8080` | Metrics port. |
| `metrics.serviceMonitor.enabled` | `false` | Create a ServiceMonitor (Prometheus Operator). |
| `metrics.serviceMonitor.interval` | `30s` | Scrape interval. |
| `metrics.serviceMonitor.labels` | `{}` | Extra labels, for example to match your Prometheus' `serviceMonitorSelector`. |
| `metrics.prometheusRule.enabled` | `false` | Create the default alerts. |
| `metrics.prometheusRule.labels` | `{}` | Extra labels, for example to match your Prometheus' `ruleSelector`. |
| `metrics.prometheusRule.staleAfterHours` | `26` | Alert threshold for a PVC with no recent successful backup. |
| `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`, `podAnnotations`, `podLabels`, `imagePullSecrets` | | Standard scheduling options for the controller. |
