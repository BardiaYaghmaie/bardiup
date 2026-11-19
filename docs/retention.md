# Retention

Bardiup keeps backups using **Grandfather-Father-Son (GFS)** rules, evaluated separately for every PVC.
You describe how far back you want recovery points; Bardiup deletes everything no rule needs, from Kubernetes and from the bucket.

```yaml
retention:
  keepLast: 3     # always keep the 3 most recent backups
  hourly: 24      # newest backup of each of the last 24 hours that have one
  daily: 7        # ... of each of the last 7 days
  weekly: 4       # ... of each of the last 4 ISO weeks
  monthly: 12     # ... of each of the last 12 months
  yearly: 5       # ... of each of the last 5 years
  custom:
    - every: 90d  # fixed-length periods: h, d or w
      keep: 8     # 8 quarters
```

## How a rule works

A rule like `daily: 7` walks the PVC's completed backups from newest to oldest and keeps the **newest backup of each calendar day**, until it has kept backups from 7 different days. A backup is kept if **any** rule keeps it, so one backup often satisfies several rules at once (the newest backup is usually `last`, `hourly`, `daily`, `weekly`, `monthly` and `yearly` at the same time).

These are the same semantics as `restic forget --keep-*` and `borg prune`. If you already know those tools, nothing here will surprise you.

Some properties worth knowing:

- **Days without backups don't use up slots.** `daily: 7` means "7 days that have a backup", not "the last 7 calendar days". If the cluster was down for a week, you still keep 7 daily recovery points.
- **The newest backup is always kept** as long as there is at least one rule.
- **It is stable.** Re-running retention on what it kept never deletes more. There's no cascading deletion over time (this is checked by a fuzz test).
- **The total number of kept backups is bounded** by the sum of all rules, and in practice is much lower because of overlap. After a year of nightly runs, `daily: 7, weekly: 4, monthly: 12, yearly: 5` keeps **20 of 365** backups (25 after five years). Hourly backups with `hourly: 24, daily: 14, weekly: 8, monthly: 12, yearly: 3` keep **52 of 8,760** after a year.
- **Periods follow `spec.timeZone`.** With `timeZone: Asia/Tehran`, "a day" starts at local midnight. The schedule uses the same zone.
- **Custom periods are epoch-aligned.** `every: 90d` splits time into 90-day blocks starting at the Unix epoch (in `spec.timeZone`).
- **No rules means keep everything.** Retention never deletes anything until you configure it.

## What gets pruned

| Backup state | Retention behaviour |
|---|---|
| `Completed` | Kept if a rule keeps it, otherwise deleted. |
| `Failed` | The newest `failedBackupsHistoryLimit` (default 3) per PVC are kept for debugging; older ones are deleted. |
| `Pending`, `Snapshotting`, `Running` | Never touched. |
| Created by hand (no `bardiup.io/backup-config` label) | Never touched. |

Deleting a `Backup` object deletes its archive and manifest from the bucket, unless `spec.deletionPolicy: Retain`.

Deleting a `BackupConfig` **does not** delete its backups. To remove them deliberately:

```bash
kubectl delete backups -l bardiup.io/backup-config=<name>
```

## Seeing why a backup is kept

Every retained backup shows which rules keep it:

```console
$ kubectl get backups
NAME                                 PVC         PHASE       SIZE       RETAINED BY                 COMPLETED   AGE
nightly-data-pg-0-20261119-020000   data-pg-0   Completed   1.2 GiB    ["last","daily","weekly","monthly","yearly"]   8h   8h
nightly-data-pg-0-20261118-020000   data-pg-0   Completed   1.2 GiB    ["last","daily"]            32h         32h
nightly-data-pg-0-20261116-020000   data-pg-0   Completed   1.1 GiB    ["daily","weekly"]          3d8h        3d8h
nightly-data-pg-0-20261031-020000   data-pg-0   Completed   1.1 GiB    ["monthly"]                 19d         19d
```

A backup without a rule in `RETAINED BY` will be pruned at the next reconcile. This makes it easy to answer "will my October backup still be here next month?" before it matters.

## Choosing a policy

| Workload | Schedule | Retention |
|---|---|---|
| Production database | `0 * * * *` | `hourly: 24, daily: 14, weekly: 8, monthly: 12, yearly: 3` |
| Typical stateful app | `0 2 * * *` | `daily: 7, weekly: 4, monthly: 6` |
| Compliance (e.g. 7 years) | `0 3 * * *` | `daily: 30, monthly: 12, yearly: 7` (plus bucket object lock) |
| Dev / staging | `0 4 * * 1-5` | `keepLast: 5` |

Storage cost is roughly `kept backups × compressed size`. Each backup is a full archive (see [the roadmap](../README.md#roadmap) for incremental backups), so GFS is how you stay at a few dozen archives instead of hundreds.
