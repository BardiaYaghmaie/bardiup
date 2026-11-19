# Security model

## Multi-tenancy

Everything is namespaced. A `BackupConfig` can only select PVCs in its own namespace, it can only reference a credentials Secret in its own namespace, and its Jobs run in that namespace. A user who can create Bardiup resources in namespace `a` can't read, back up or restore anything in namespace `b`.

With the default `aggregateToDefaultRoles: true`, the built-in `admin` and `edit` ClusterRoles can manage Bardiup resources and `view` can read them. Namespace owners get self-service backups with the RBAC they already have, and nobody needs cluster-admin.

Mover pods are created by the controller but still go through the namespace's admission. Pod Security Admission, Kyverno or Gatekeeper policies apply to them as they would to any workload in that namespace, so Bardiup can't be used to get around a namespace's policy.

## Controller permissions

The controller's ClusterRole is generated from the code ([charts/bardiup/files/role.yaml](../charts/bardiup/files/role.yaml)) and uses no wildcards:

| Resource | Verbs | Why |
|---|---|---|
| backupconfigs, restores | get, list, watch (+ status) | Reconcile them. |
| backups | full | Create on schedule, prune by retention. |
| jobs | get, list, watch, create, delete | Run and clean up movers. Only Jobs labelled `app.kubernetes.io/managed-by=bardiup` are cached. |
| persistentvolumeclaims | get, list, watch, create, delete | Select PVCs, create restore targets and snapshot clones. |
| pods | list | Find the node of a workload using a `ReadWriteOnce` volume and read mover results. |
| secrets, namespaces | **get only** | Read the referenced credentials Secret; detect `restricted` Pod Security. No list or watch on secrets. |
| volumesnapshots | get, create, delete | Snapshot method. |
| events | create, patch | Kubernetes events. |

The controller runs as non-root with a read-only root filesystem, all capabilities dropped and the `RuntimeDefault` seccomp profile.

## Mover pods

Reading every file of an arbitrary volume needs to bypass file permissions. By default the mover runs as UID 0 with **only** these capabilities:

- backup: `DAC_OVERRIDE` (read any file). The volume is mounted **read-only**.
- restore: `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `FSETID` (write files and restore ownership and modes).

All other capabilities are dropped, privilege escalation is disabled, the root filesystem is read-only, and no service account token is mounted unless you set `job.serviceAccountName`. This fits the **baseline** Pod Security Standard. File-level backup tools usually run a DaemonSet with `hostPath` access to every pod volume on the node. Bardiup instead mounts only the one volume it's working on, through the normal CSI path, and only while the backup runs.

### Namespaces enforcing `restricted`

The `restricted` standard forbids UID 0. Bardiup detects `pod-security.kubernetes.io/enforce: restricted` and fails the backup immediately with an explanation, instead of leaving a Job stuck. Run the mover as the user that owns the data:

```yaml
spec:
  job:
    securityContext:
      runAsNonRoot: true
      runAsUser: 999          # e.g. the postgres user
      runAsGroup: 999
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities: {drop: [ALL]}
      seccompProfile: {type: RuntimeDefault}
```

Files that user can't read make the backup fail, so you never end up with a silently incomplete backup.

## Data protection

- **In transit:** HTTPS to the object store (use an `https://` endpoint for self-hosted stores). `insecureSkipTLSVerify` exists for labs only.
- **At rest:** `serverSideEncryption: AES256` or `aws:kms` with `kmsKeyID`. Bucket policies, versioning and **S3 Object Lock** (WORM) work as usual and are the recommended way to protect backups against deletion by a compromised cluster.
- **Integrity:** every archive's SHA-256 is recorded in the `Backup` status and in the manifest, and verified on restore.
- **Archive extraction** uses Go's `os.Root`, so a tampered archive can't write outside the target volume through `..`, absolute paths or symlinks. This is covered by tests and a fuzz test.

## Credentials

Prefer workload identity (IRSA, EKS Pod Identity, GKE/Azure equivalents with S3-compatible gateways) over static keys. With static keys, scope them to the bucket and prefix the namespace uses. Each namespace can have its own credentials and its own bucket.

## Reporting vulnerabilities

See [SECURITY.md](../SECURITY.md).
