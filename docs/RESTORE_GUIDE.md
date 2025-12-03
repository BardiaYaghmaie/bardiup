# Bardiup Restore Guide

> **A backup is only as good as your ability to restore from it.**

This guide covers everything you need to know about restoring data from Bardiup backups, including step-by-step procedures, common scenarios, and best practices.

---

## Table of Contents

1. [Understanding Bardiup Backups](#understanding-bardiup-backups)
2. [Quick Restore (TL;DR)](#quick-restore-tldr)
3. [Finding the Right Backup](#finding-the-right-backup)
4. [Restore Procedures](#restore-procedures)
   - [Restore to Same PVC](#restore-to-same-pvc)
   - [Restore to New PVC](#restore-to-new-pvc)
   - [Restore to Different Namespace](#restore-to-different-namespace)
   - [Cross-Cluster Restore](#cross-cluster-restore)
5. [Restore Job Templates](#restore-job-templates)
6. [Verification & Testing](#verification--testing)
7. [Common Scenarios](#common-scenarios)
8. [Troubleshooting](#troubleshooting)
9. [Best Practices](#best-practices)

---

## Understanding Bardiup Backups

### What Gets Backed Up

Bardiup creates **tar.gz archives** of your PVC contents and uploads them to S3-compatible storage.

Each backup includes:
- All files and directories from the PVC
- File permissions and ownership (where supported)
- Symbolic links

Each backup does **not** include:
- PVC metadata (you need to recreate the PVC)
- StorageClass-specific features
- In-flight database transactions (use hooks when available)

### Backup Storage Structure

```
s3://bucket/
└── <prefix>/
    └── <namespace>/
        └── <pvc-name>/
            ├── backup-2024-01-15T10-00-00Z.tar.gz
            ├── backup-2024-01-16T10-00-00Z.tar.gz
            └── backup-2024-01-17T10-00-00Z.tar.gz
```

### Backup Resource

Each backup is represented by a Kubernetes `Backup` custom resource:

```yaml
apiVersion: bardiup.io/v1alpha1
kind: Backup
metadata:
  name: postgres-data-20240115-100000
  namespace: default
  labels:
    bardiup.io/config: postgres-backup
    bardiup.io/pvc: postgres-data
    bardiup.io/retention: daily
spec:
  pvcName: postgres-data
  backupConfigRef: postgres-backup
  storageLocation:
    path: default/postgres-data/backup-2024-01-15T10-00-00Z.tar.gz
status:
  phase: Completed
  startTime: "2024-01-15T10:00:00Z"
  completionTime: "2024-01-15T10:02:30Z"
  sizeBytes: 1073741824
```

---

## Quick Restore (TL;DR)

For those who just need to restore quickly:

```bash
# 1. Find your backup
kubectl get backups -l bardiup.io/pvc=<your-pvc-name>

# 2. Get the storage path
kubectl get backup <backup-name> -o jsonpath='{.spec.storageLocation.path}'

# 3. Scale down the application using the PVC
kubectl scale deployment <your-app> --replicas=0

# 4. Run the restore job (edit the template below)
kubectl apply -f restore-job.yaml

# 5. Wait for completion
kubectl wait --for=condition=complete job/restore-job --timeout=600s

# 6. Scale the application back up
kubectl scale deployment <your-app> --replicas=1
```

---

## Finding the Right Backup

### List All Backups for a PVC

```bash
# By PVC name
kubectl get backups -l bardiup.io/pvc=postgres-data

# By BackupConfig name
kubectl get backups -l bardiup.io/config=postgres-backup

# With details
kubectl get backups -l bardiup.io/pvc=postgres-data -o wide
```

### Filter by Retention Tier

```bash
# Get daily backups only
kubectl get backups -l bardiup.io/pvc=postgres-data,bardiup.io/retention=daily

# Get monthly backups
kubectl get backups -l bardiup.io/pvc=postgres-data,bardiup.io/retention=monthly
```

### Sort by Time

```bash
# Most recent first
kubectl get backups -l bardiup.io/pvc=postgres-data \
  --sort-by='.status.completionTime'
```

### Get Full Details

```bash
kubectl describe backup <backup-name>

# Key fields to note:
# - spec.storageLocation.path  → S3 object key
# - status.phase               → Should be "Completed"
# - status.completionTime      → When backup finished
# - status.sizeBytes           → Backup size
```

### Find Backup Directly in S3

If `Backup` resources are missing, you can list directly:

```bash
# Using AWS CLI
aws s3 ls s3://bucket/prefix/namespace/pvc-name/ --recursive

# Using MinIO client
mc ls minio/bucket/prefix/namespace/pvc-name/
```

---

## Restore Procedures

### Restore to Same PVC

**Scenario:** Restore data to the original PVC (e.g., after accidental deletion).

**Prerequisites:**
- Application using the PVC must be stopped
- PVC must exist (recreate if necessary)

**Steps:**

1. **Stop the application:**
   ```bash
   kubectl scale deployment myapp --replicas=0
   # Or for StatefulSets:
   kubectl scale statefulset myapp --replicas=0
   ```

2. **Verify PVC is not in use:**
   ```bash
   kubectl get pods -o json | jq '.items[] | select(.spec.volumes[].persistentVolumeClaim.claimName == "postgres-data") | .metadata.name'
   ```

3. **Create and run restore job:**
   ```yaml
   apiVersion: batch/v1
   kind: Job
   metadata:
     name: restore-postgres-data
     namespace: default
   spec:
     ttlSecondsAfterFinished: 3600
     template:
       spec:
         serviceAccountName: default
         containers:
         - name: restore
           image: amazon/aws-cli:2.13.0
           command: ["/bin/sh", "-c"]
           args:
           - |
             set -e
             echo "Downloading backup..."
             aws s3 cp s3://bucket/prefix/default/postgres-data/backup-2024-01-15T10-00-00Z.tar.gz /tmp/backup.tar.gz
             
             echo "Clearing existing data..."
             rm -rf /data/*
             
             echo "Extracting backup..."
             tar -xzf /tmp/backup.tar.gz -C /data
             
             echo "Restore complete!"
             ls -la /data
           env:
           - name: AWS_ACCESS_KEY_ID
             valueFrom:
               secretKeyRef:
                 name: s3-credentials
                 key: accessKeyId
           - name: AWS_SECRET_ACCESS_KEY
             valueFrom:
               secretKeyRef:
                 name: s3-credentials
                 key: secretAccessKey
           - name: AWS_DEFAULT_REGION
             value: "us-west-2"
           volumeMounts:
           - name: data
             mountPath: /data
         volumes:
         - name: data
           persistentVolumeClaim:
             claimName: postgres-data
         restartPolicy: Never
     backoffLimit: 2
   ```

4. **Monitor the restore:**
   ```bash
   kubectl logs -f job/restore-postgres-data
   ```

5. **Verify and restart application:**
   ```bash
   kubectl wait --for=condition=complete job/restore-postgres-data --timeout=600s
   kubectl scale deployment myapp --replicas=1
   ```

---

### Restore to New PVC

**Scenario:** Create a new PVC and restore data to it (e.g., for testing or migration).

1. **Create a new PVC:**
   ```yaml
   apiVersion: v1
   kind: PersistentVolumeClaim
   metadata:
     name: postgres-data-restored
     namespace: default
   spec:
     accessModes:
       - ReadWriteOnce
     resources:
       requests:
         storage: 10Gi  # Must be >= original PVC size
     storageClassName: standard
   ```

2. **Run restore job targeting new PVC:**
   ```yaml
   # Same as above, but change:
   volumes:
   - name: data
     persistentVolumeClaim:
       claimName: postgres-data-restored  # New PVC
   ```

3. **Update application to use new PVC:**
   ```bash
   kubectl patch deployment myapp -p '{"spec":{"template":{"spec":{"volumes":[{"name":"data","persistentVolumeClaim":{"claimName":"postgres-data-restored"}}]}}}}'
   ```

---

### Restore to Different Namespace

**Scenario:** Restore production backup to staging/development.

1. **Ensure credentials secret exists in target namespace:**
   ```bash
   kubectl get secret s3-credentials -n production -o yaml | \
     sed 's/namespace: production/namespace: staging/' | \
     kubectl apply -f -
   ```

2. **Create PVC in target namespace:**
   ```yaml
   apiVersion: v1
   kind: PersistentVolumeClaim
   metadata:
     name: postgres-data
     namespace: staging  # Target namespace
   spec:
     accessModes:
       - ReadWriteOnce
     resources:
       requests:
         storage: 10Gi
   ```

3. **Run restore job in target namespace:**
   ```yaml
   metadata:
     name: restore-from-prod
     namespace: staging  # Target namespace
   # ... rest of job spec
   ```

---

### Cross-Cluster Restore

**Scenario:** Restore backup to a completely different Kubernetes cluster.

**Prerequisites:**
- Access to S3 bucket from target cluster
- S3 credentials available in target cluster

1. **Set up credentials in target cluster:**
   ```yaml
   apiVersion: v1
   kind: Secret
   metadata:
     name: s3-credentials
     namespace: default
   type: Opaque
   stringData:
     accessKeyId: "your-access-key"
     secretAccessKey: "your-secret-key"
   ```

2. **Create PVC in target cluster:**
   ```bash
   kubectl apply -f pvc.yaml --context=target-cluster
   ```

3. **Get backup path from source cluster:**
   ```bash
   kubectl get backup <backup-name> -o jsonpath='{.spec.storageLocation.path}' --context=source-cluster
   ```

4. **Run restore job in target cluster:**
   ```bash
   kubectl apply -f restore-job.yaml --context=target-cluster
   ```

---

## Restore Job Templates

### AWS S3 Restore Template

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: restore-${PVC_NAME}
  namespace: ${NAMESPACE}
spec:
  ttlSecondsAfterFinished: 3600
  template:
    spec:
      containers:
      - name: restore
        image: amazon/aws-cli:2.13.0
        command: ["/bin/sh", "-c"]
        args:
        - |
          set -e
          aws s3 cp s3://${BUCKET}/${BACKUP_PATH} /tmp/backup.tar.gz
          rm -rf /data/*
          tar -xzf /tmp/backup.tar.gz -C /data
          echo "Restore complete"
        env:
        - name: AWS_ACCESS_KEY_ID
          valueFrom:
            secretKeyRef:
              name: ${CREDENTIALS_SECRET}
              key: accessKeyId
        - name: AWS_SECRET_ACCESS_KEY
          valueFrom:
            secretKeyRef:
              name: ${CREDENTIALS_SECRET}
              key: secretAccessKey
        - name: AWS_DEFAULT_REGION
          value: "${REGION}"
        volumeMounts:
        - name: data
          mountPath: /data
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: ${PVC_NAME}
      restartPolicy: Never
  backoffLimit: 2
```

### MinIO Restore Template

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: restore-${PVC_NAME}
  namespace: ${NAMESPACE}
spec:
  ttlSecondsAfterFinished: 3600
  template:
    spec:
      containers:
      - name: restore
        image: amazon/aws-cli:2.13.0
        command: ["/bin/sh", "-c"]
        args:
        - |
          set -e
          aws --endpoint-url ${MINIO_ENDPOINT} s3 cp s3://${BUCKET}/${BACKUP_PATH} /tmp/backup.tar.gz
          rm -rf /data/*
          tar -xzf /tmp/backup.tar.gz -C /data
          echo "Restore complete"
        env:
        - name: AWS_ACCESS_KEY_ID
          valueFrom:
            secretKeyRef:
              name: minio-credentials
              key: accessKeyId
        - name: AWS_SECRET_ACCESS_KEY
          valueFrom:
            secretKeyRef:
              name: minio-credentials
              key: secretAccessKey
        - name: AWS_DEFAULT_REGION
          value: "us-east-1"  # Dummy region for MinIO
        volumeMounts:
        - name: data
          mountPath: /data
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: ${PVC_NAME}
      restartPolicy: Never
  backoffLimit: 2
```

### Restore Script (bash)

Save as `restore.sh`:

```bash
#!/bin/bash
set -e

# Configuration
NAMESPACE="${1:?Usage: restore.sh <namespace> <backup-name> <target-pvc>}"
BACKUP_NAME="${2:?Usage: restore.sh <namespace> <backup-name> <target-pvc>}"
TARGET_PVC="${3:?Usage: restore.sh <namespace> <backup-name> <target-pvc>}"
BUCKET="${BUCKET:-backup-bucket}"
CREDENTIALS_SECRET="${CREDENTIALS_SECRET:-s3-credentials}"
REGION="${REGION:-us-west-2}"

# Get backup path
BACKUP_PATH=$(kubectl get backup "$BACKUP_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.storageLocation.path}')
if [ -z "$BACKUP_PATH" ]; then
    echo "Error: Could not find backup path"
    exit 1
fi

echo "Restoring backup: $BACKUP_PATH"
echo "To PVC: $TARGET_PVC in namespace $NAMESPACE"

# Generate restore job
cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: restore-$(date +%s)
  namespace: $NAMESPACE
spec:
  ttlSecondsAfterFinished: 3600
  template:
    spec:
      containers:
      - name: restore
        image: amazon/aws-cli:2.13.0
        command: ["/bin/sh", "-c"]
        args:
        - |
          set -e
          aws s3 cp s3://$BUCKET/$BACKUP_PATH /tmp/backup.tar.gz
          rm -rf /data/*
          tar -xzf /tmp/backup.tar.gz -C /data
          echo "Restore complete"
        env:
        - name: AWS_ACCESS_KEY_ID
          valueFrom:
            secretKeyRef:
              name: $CREDENTIALS_SECRET
              key: accessKeyId
        - name: AWS_SECRET_ACCESS_KEY
          valueFrom:
            secretKeyRef:
              name: $CREDENTIALS_SECRET
              key: secretAccessKey
        - name: AWS_DEFAULT_REGION
          value: "$REGION"
        volumeMounts:
        - name: data
          mountPath: /data
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: $TARGET_PVC
      restartPolicy: Never
  backoffLimit: 2
EOF

echo "Restore job created. Monitor with: kubectl logs -f job/restore-... -n $NAMESPACE"
```

---

## Verification & Testing

### Verify Restore Integrity

After restoring, verify data integrity:

```bash
# Check file count and sizes
kubectl exec -it deployment/myapp -- find /data -type f | wc -l
kubectl exec -it deployment/myapp -- du -sh /data

# For databases, run integrity checks
kubectl exec -it deployment/postgres -- pg_isready
kubectl exec -it deployment/postgres -- psql -c "SELECT count(*) FROM important_table;"
```

### Regular Restore Testing

**Best Practice:** Schedule regular restore tests to verify backup integrity.

```yaml
# Monthly restore test CronJob
apiVersion: batch/v1
kind: CronJob
metadata:
  name: monthly-restore-test
  namespace: testing
spec:
  schedule: "0 3 1 * *"  # 3 AM on the 1st of each month
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: test-restore
            image: your-test-image
            command: ["/bin/sh", "-c"]
            args:
            - |
              # Get latest backup
              BACKUP=$(kubectl get backups -l bardiup.io/pvc=prod-data --sort-by=.status.completionTime -o jsonpath='{.items[-1].metadata.name}')
              # Run restore to test PVC
              ./restore.sh testing $BACKUP test-pvc
              # Verify data
              ./verify-data.sh
          restartPolicy: Never
```

---

## Common Scenarios

### Scenario 1: Accidental Data Deletion

**Problem:** User accidentally deleted important files.

**Solution:**
1. Identify when deletion occurred
2. Find the most recent backup before deletion
3. Restore to a new PVC
4. Copy needed files back

```bash
# Find backup from before deletion
kubectl get backups -l bardiup.io/pvc=data-pvc --sort-by=.status.completionTime

# Restore to temporary PVC
./restore.sh default backup-before-deletion temp-restore-pvc

# Copy files back (from a pod with both PVCs mounted)
kubectl cp temp-pod:/restore/important-file.txt local-file.txt
kubectl cp local-file.txt prod-pod:/data/important-file.txt
```

### Scenario 2: Database Corruption

**Problem:** Database files became corrupted.

**Solution:**
1. Stop the database
2. Restore entire PVC from backup
3. Restart database

```bash
kubectl scale statefulset postgres --replicas=0
kubectl wait --for=delete pod/postgres-0
./restore.sh default postgres-backup-20240115 postgres-data
kubectl scale statefulset postgres --replicas=1
```

### Scenario 3: Migration to New Storage Class

**Problem:** Need to move data to faster storage.

**Solution:**
1. Create new PVC with desired storage class
2. Restore backup to new PVC
3. Update application

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: data-fast
spec:
  storageClassName: fast-ssd
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 100Gi
```

### Scenario 4: Point-in-Time Recovery

**Problem:** Need data as it was at a specific time.

**Solution:**
1. List backups with timestamps
2. Find backup closest to desired time
3. Restore that specific backup

```bash
# List backups with timestamps
kubectl get backups -l bardiup.io/pvc=mydata \
  -o custom-columns='NAME:.metadata.name,TIME:.status.completionTime'

# Choose the appropriate backup and restore
./restore.sh default backup-closest-to-time target-pvc
```

---

## Troubleshooting

### Restore Job Fails to Start

**Symptom:** Job pod stays in `Pending` state.

**Causes & Solutions:**
- **PVC still in use:** Scale down application first
- **Insufficient resources:** Check node resources
- **PVC doesn't exist:** Create the target PVC

```bash
kubectl describe pod -l job-name=restore-job
```

### Permission Denied Errors

**Symptom:** `tar: Cannot create: Permission denied`

**Solutions:**
1. Run as root in the restore container:
   ```yaml
   securityContext:
     runAsUser: 0
   ```
2. Check PVC's fsGroup settings
3. Verify the target filesystem supports the permissions

### S3 Access Denied

**Symptom:** `An error occurred (AccessDenied)`

**Solutions:**
1. Verify credentials secret exists and has correct data
2. Check IAM permissions include `s3:GetObject`
3. Verify bucket name and region are correct
4. For MinIO, ensure `forcePathStyle` is enabled

### Backup File Not Found

**Symptom:** `The specified key does not exist`

**Solutions:**
1. Verify the backup path is correct:
   ```bash
   kubectl get backup <name> -o jsonpath='{.spec.storageLocation.path}'
   ```
2. List objects in S3 to find actual path:
   ```bash
   aws s3 ls s3://bucket/prefix/ --recursive
   ```
3. Check if backup was deleted by retention policy

### Partial Restore / Corrupted Data

**Symptom:** Files missing or corrupted after restore.

**Solutions:**
1. Check restore job completed successfully
2. Verify backup integrity by downloading and testing locally:
   ```bash
   aws s3 cp s3://bucket/path/backup.tar.gz .
   tar -tzf backup.tar.gz  # List contents
   ```
3. Try restoring from a different backup

---

## Best Practices

### Before You Need to Restore

1. **Test restores regularly** - Don't wait for an emergency
2. **Document your restore process** - Team members should know how
3. **Keep restore job templates ready** - Have them in version control
4. **Monitor backup success** - Failed backups can't be restored!

### During Restore

1. **Always stop applications first** - Avoid data conflicts
2. **Use a new PVC when possible** - Safer than overwriting
3. **Verify before switching** - Test restored data works
4. **Keep the old PVC** - Until you've verified the restore

### After Restore

1. **Verify data integrity** - Run application-specific checks
2. **Update documentation** - Record what happened and why
3. **Review backup strategy** - Was frequency sufficient?
4. **Test more frequently** - If this was your first real restore

---

## Getting Help

- **Documentation:** [User Guide](USER_GUIDE.md)
- **Examples:** See `examples/` directory
- **Issues:** Open a GitHub issue

---

<p align="center">
  Remember: <strong>Untested backups are not backups.</strong>
  <br>
  Schedule regular restore tests!
</p>

