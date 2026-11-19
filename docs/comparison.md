# Bardiup compared

Bardiup does one thing: it gets the **data in your PersistentVolumeClaims** off the cluster, keeps the right copies for as long as you need them, and lets the team that owns the data run it. This page helps you choose. It's explicit about where another tool is the better fit, because the wrong backup tool is worse than none.

Statements about other tools describe Velero 1.18, K8up 2.x, VolSync 0.x, Longhorn 1.x and Gemini as documented by their projects. If something is out of date, please [open an issue](https://github.com/BardiaYaghmaie/bardiup/issues).

## Pick in 30 seconds

| If you need... | Use |
|---|---|
| Backups of Kubernetes **resources**, or to migrate whole namespaces between clusters | **Velero** |
| Deduplicated, encrypted backups of very large volumes that change slowly | **Velero** or **K8up** |
| Continuous **replication** of volumes to another cluster | **VolSync** |
| Backups of Longhorn volumes and nothing else | **Longhorn**'s built-in backups |
| Scheduled **snapshots** only, kept on the same storage system | **Gemini** or your CSI driver |
| Volume data off the cluster, with daily/weekly/monthly/yearly retention, run by each team, restorable with `tar` | **Bardiup** |

Most clusters keep their manifests in Git already, so the data in the volumes is the part they can't recreate. Bardiup is built around that.

## At a glance

| | Bardiup | Velero | K8up | VolSync | Longhorn | Gemini |
|---|---|---|---|---|---|---|
| **Main job** | PVC backups with retention | Cluster resources + volumes | PVC and application backups | Volume replication and backup | Backups of Longhorn volumes | Scheduled CSI snapshots |
| **Copies data off the cluster** | Yes | Yes | Yes | Yes | Yes | No, snapshots stay on the storage system |
| **Retention** | Calendar GFS per PVC (`last`, `hourly`…`yearly`, custom periods), time-zone aware | One TTL per backup | restic `keep*` rules | restic `retain` rules | A count per recurring job | `every`/`keep` tiers |
| **Shows why a backup is kept** | Yes, `status.retainedBy` | No | No | No | No | No |
| **Namespace self-service** | Yes, namespaced CRDs aggregated into `admin`/`edit`/`view` | No, objects live in Velero's namespace | Yes, namespaced `Schedule` | Yes, namespaced `ReplicationSource` | No, cluster-level `RecurringJob` | Yes, namespaced `SnapshotGroup` |
| **Storage format** | `tar.gz` + JSON manifest + SHA-256 | Kopia repository | restic repository | restic repository, or a live copy | Longhorn backup blocks | CSI VolumeSnapshot |
| **Restore without the tool** | `tar` | Kopia CLI + repository password | restic + repository password | restic + repository password | No | Not applicable |
| **Deduplication / incremental** | No, each backup is a full compressed archive | Yes | Yes | Yes (restic) | Yes, block-level | Depends on the storage |
| **Runs on every node** | Nothing | node-agent DaemonSet for file-system backups | Nothing | Nothing | Longhorn itself | Nothing |
| **Works with** | Any filesystem PVC; CSI snapshots optional | Any | Any | Any | Longhorn volumes only | CSI drivers with snapshot support |

## Bardiup vs Velero

Velero is the standard tool for backing up and migrating **whole clusters**: resources and volumes, with plugins for every major cloud. If you need that, use Velero. Bardiup is narrower on purpose, and the narrow scope buys three things.

### 1. Retention you'd design yourself

Velero gives every backup a TTL, set by the schedule that created it (720 hours by default). Tiered retention therefore means several schedules, each running its own backup:

```bash
velero schedule create daily   --schedule "0 2 * * *" --ttl 168h   --include-namespaces shop
velero schedule create weekly  --schedule "0 3 * * 0" --ttl 672h   --include-namespaces shop
velero schedule create monthly --schedule "0 4 1 * *" --ttl 8760h  --include-namespaces shop
velero schedule create yearly  --schedule "0 5 1 1 *" --ttl 43800h --include-namespaces shop
```

When the 1st of a month is a Sunday, that's three backup runs of the same data in three hours (four on a Sunday 1 January), and four schedules to keep in sync whenever the namespace changes. Kopia deduplication limits the extra storage, but the extra runs remain.

With Bardiup it's one schedule, and each backup is kept by every rule it satisfies:

```yaml
schedule: "0 2 * * *"
retention: {daily: 7, weekly: 4, monthly: 12, yearly: 5}
```

There's also a difference in failure behaviour. A TTL is wall-clock time: if backups fail for eight days, the last good daily backup expires anyway. Bardiup's rules count **periods that have a backup**, so `daily: 7` always means the seven most recent days that *have* a successful backup. A long outage never deletes your last good copies.

### 2. Backups owned by the team that owns the data

Velero's `Backup`, `Schedule` and `Restore` objects live in Velero's namespace. Anyone who can create them can back up or restore any namespace, so in practice only cluster admins do. Self-service needs an extra layer, such as OADP's non-admin controller on OpenShift.

Bardiup's resources are namespaced. A team creates a `BackupConfig` next to its database, with its own bucket and credentials, using the `edit` role it already has. It can't select PVCs, Secrets or buckets of another namespace.

### 3. Nothing privileged on your nodes

Velero's file-system backup and data mover need the node-agent DaemonSet on every node, with `hostPath` mounts of `/var/lib/kubelet/pods` and `/var/lib/kubelet/plugins`, running as root (and privileged on SELinux hosts). Bardiup runs one short-lived Job per volume, which mounts that one volume through the normal CSI path. Between backups, nothing of Bardiup runs on your nodes.

### Side by side

| | Bardiup | Velero |
|---|---|---|
| Scope | PVC data | Kubernetes resources + PVC data |
| Retention | Calendar GFS per PVC, time-zone aware, `status.retainedBy` | Per-backup TTL from the schedule |
| Self-service | Namespaced CRDs; per-namespace credentials and buckets | Central namespace; cluster-admin in practice |
| Format | `tar.gz` + manifest + SHA-256 | Kopia repository, encrypted with a repository password |
| Incremental / dedup | No | Yes |
| Crash-consistent | `method: snapshot` (CSI snapshot → clone → archive) | CSI snapshots, optionally moved to object storage |
| Application hooks | Not yet ([roadmap](../README.md#roadmap)) | Pre/post exec hooks |
| Storage backends | S3 and S3-compatible | AWS, Azure, GCP and others through plugins |
| Restore | Into a new or existing PVC, or from a raw bucket key | Namespace mapping, resource filtering, and more |
| Install | One Deployment; one 14 MB image for controller and mover | Server Deployment + node-agent DaemonSet + provider plugin images |
| Observability | Metrics, ready-made PrometheusRule, events, conditions | Metrics, events |

## Bardiup vs K8up

K8up is the closest relative: namespaced, Job-based, with restic-style `keep*` retention. The two differ in what they optimise for.

- **K8up** stores data in a restic repository, so backups are deduplicated and encrypted, and it can stream application dumps through the `k8up.io/backupcommand` pod annotation. Restoring outside K8up needs restic and the repository password. Retention runs as restic `prune` over the repository.
- **Bardiup** stores each backup as a standalone archive, so you can restore it with `tar` and delete it without touching any other backup. Every backup is a Kubernetes object with its size, checksum and the retention rules keeping it. It also adds CSI-snapshot backups and pins mover pods to the node of an in-use `ReadWriteOnce` volume.

Pick K8up if deduplication or application dump commands matter most. Pick Bardiup if you want an open format and visible, per-backup retention.

## Bardiup vs VolSync

VolSync is built for **asynchronous replication**: keeping a copy of a volume up to date in another cluster, with rsync, rclone, restic or Syncthing movers. Its restic mover also does backups with restic's `retain` rules. If you want a warm standby volume in another cluster, use VolSync. If you want point-in-time backups with long retention in a format anyone can read, use Bardiup.

## Bardiup vs Longhorn backups

Longhorn backs up its own volumes to S3 or NFS with block-level incrementals, which is efficient. It only works for Longhorn volumes, retention is a count per recurring job, and restores need Longhorn. Bardiup works with any filesystem volume, so it fits mixed or managed storage (EBS, Ceph, local-path, cloud disks) and survives a change of storage system.

## Bardiup vs snapshot schedulers (Gemini, CSI snapshots)

CSI snapshots are fast and cheap, and tools like Gemini add tiered `every`/`keep` schedules on top. But snapshots usually live on the same storage system as the volume, so they don't protect you from losing that system, its account or its region. Bardiup's `snapshot` method uses a CSI snapshot as a consistent source and then ships the data to object storage.

## Bardiup vs commercial platforms

Kasten K10, Portworx Backup, Trilio and CloudCasa are full data-protection platforms with UIs, application blueprints, policy engines and support contracts. If you need all of that, they're worth their price. Bardiup is a single Apache-2.0 operator you can read in an afternoon.

## Bardiup vs cron + `tar`

This is what Bardiup replaces. You keep the open format and gain:

- scheduling and node-aware mounting
- retention and checksums
- restores into new PVCs
- metrics and alerts
- multi-tenancy

## What Bardiup doesn't do (yet)

- **Kubernetes resources.** Use GitOps or Velero.
- **Deduplication or incremental backups.** Each backup is a full compressed archive. GFS keeps the number of archives low (20 for a year of nightly backups), but very large volumes with small daily changes are cheaper with a deduplicating tool.
- **Application hooks.** For databases, use `method: snapshot` (crash-consistent) or back up a dump volume. Hooks are on the [roadmap](../README.md#roadmap).
- **Client-side encryption.** Use server-side encryption (SSE-S3 or SSE-KMS) for now. Client-side encryption is on the roadmap.
- **Non-S3 backends.** Azure Blob and GCS are on the roadmap.
- **Block-mode volumes.**

## Using them together

A common setup:

- Git (or Velero with `--snapshot-volumes=false --default-volumes-to-fs-backup=false`) for cluster resources.
- Bardiup for volume data and its retention.
- Bucket versioning or S3 Object Lock as the last line of defence.
