# Restore and disaster recovery

## Restore into a new PVC (recommended)

Restoring next to the live volume is the safest way to recover: nothing is overwritten, and you can inspect the result before switching over.

```bash
kubectl get backups -l bardiup.io/pvc=data-postgres-0
```

```yaml
apiVersion: bardiup.io/v1alpha1
kind: Restore
metadata: {name: pg-rollback}
spec:
  backupName: nightly-data-postgres-0-20261119-020000
  target: {pvcName: data-postgres-0-restored}
```

```console
$ kubectl get restore pg-rollback -w
NAME          BACKUP                                    TARGET PVC                 PHASE       AGE
pg-rollback   nightly-data-postgres-0-20261119-020000   data-postgres-0-restored   Running     4s
pg-rollback   nightly-data-postgres-0-20261119-020000   data-postgres-0-restored   Completed   41s
```

The new PVC gets the source volume's storage class, access modes and capacity, unless `target` overrides them. It's annotated with `bardiup.io/restored-from`. The archive's SHA-256 is verified during extraction, and file ownership, permissions, timestamps, symlinks and hard links are restored.

To switch a StatefulSet over, scale it down, then either point it at the restored PVC or restore in place (below).

## Restore in place

```yaml
spec:
  backupName: nightly-data-postgres-0-20261119-020000
  target:
    pvcName: data-postgres-0
    cleanTarget: true   # wipe the volume first, so it matches the backup exactly
```

Scale the workload to zero first. Bardiup will restore into a volume that's still mounted (the mover pod is scheduled next to the workload for `ReadWriteOnce` volumes), but an application writing at the same time will see inconsistent data. Without `cleanTarget`, restored files overwrite existing ones and unrelated files are left alone.

## Disaster recovery: new cluster, same bucket

Every archive has a JSON manifest next to it, so the bucket describes itself. You don't need the original cluster or its `Backup` objects.

```console
$ aws s3 ls --recursive s3://backups/prod/shop/data-postgres-0/
2026-11-19 02:01:12  1288490188 prod/shop/data-postgres-0/nightly-data-postgres-0-20261119-020000.tar.gz
2026-11-19 02:01:12         702 prod/shop/data-postgres-0/nightly-data-postgres-0-20261119-020000.tar.gz.json
```

1. Install Bardiup in the new cluster and create the credentials secret in the target namespace.
2. Restore straight from the object key:

```yaml
apiVersion: bardiup.io/v1alpha1
kind: Restore
metadata: {name: dr-postgres, namespace: shop}
spec:
  source:
    objectKey: prod/shop/data-postgres-0/nightly-data-postgres-0-20261119-020000.tar.gz
    storage:
      s3: {bucket: backups, region: eu-central-1, credentialsSecretRef: {name: s3-credentials}}
  target:
    pvcName: data-postgres-0
    size: 20Gi
    storageClassName: gp3
```

If the manifest exists, its checksum is enforced. The same mechanism moves data between namespaces or clusters.

## Restore without Bardiup

Archives are ordinary gzip-compressed POSIX tar files. If Bardiup isn't available, any machine with `tar` and an S3 client can restore them:

```bash
aws s3 cp s3://backups/prod/shop/data-postgres-0/nightly-data-postgres-0-20261119-020000.tar.gz - \
  | tee >(sha256sum >&2) | tar -xzpf - -C /mnt/restore --numeric-owner
aws s3 cp s3://backups/prod/shop/data-postgres-0/nightly-data-postgres-0-20261119-020000.tar.gz.json - | jq .sha256
```

Compare the two checksums. This is a deliberate design choice: your recovery doesn't depend on a running operator, a repository format or an encryption key you might have lost.

## Manifest format

```json
{
  "formatVersion": 1,
  "bardiupVersion": "v0.1.0",
  "backup": "nightly-data-postgres-0-20261119-020000",
  "backupConfig": "nightly",
  "namespace": "shop",
  "pvc": "data-postgres-0",
  "source": {"storageClassName": "gp3", "accessModes": ["ReadWriteOnce"], "capacity": "20Gi"},
  "method": "copy",
  "startedAt": "2026-11-19T02:00:03Z",
  "completedAt": "2026-11-19T02:01:12Z",
  "sha256": "9f2c...",
  "sizeBytes": 1288490188,
  "files": 1832,
  "dataBytes": 4123456789,
  "format": "tar+gzip"
}
```
