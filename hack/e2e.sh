#!/usr/bin/env bash
# Functional end-to-end test: backup, retention, restore, disaster recovery, deletion.
#   E2E_SNAPSHOT=1  also installs the CSI hostpath driver and tests method: snapshot.
#   E2E_KEEP=1      keeps the cluster afterwards.
# See hack/lib.sh for running against an existing cluster.
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"

setup_cluster 1
deploy_s3

if [[ -n "${E2E_SNAPSHOT:-}" ]]; then
  log "Installing CSI snapshot controller and hostpath driver"
  SNAP=v8.7.0
  for f in volumesnapshotclasses volumesnapshotcontents volumesnapshots; do
    kubectl apply -f "https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/$SNAP/client/config/crd/snapshot.storage.k8s.io_$f.yaml"
  done
  kubectl apply -k "https://github.com/kubernetes-csi/external-snapshotter/deploy/kubernetes/snapshot-controller?ref=$SNAP"
  git clone -q --depth 1 --branch v1.19.0 https://github.com/kubernetes-csi/csi-driver-host-path "$WORK/hostpath"
  "$WORK/hostpath/deploy/kubernetes-latest/deploy.sh"
  kubectl apply -f "$WORK/hostpath/examples/csi-storageclass.yaml"
  kubectl apply -f "$WORK/hostpath/examples/csi-volumesnapshotclass.yaml"
fi

install_bardiup

log "Creating a workload that keeps its RWO volume mounted"
new_ns app
kubectl -n app apply -f - <<'EOF'
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data, labels: {backup: "yes"}}
spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 1Gi}}}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: writer}
spec:
  selector: {matchLabels: {app: writer}}
  template:
    metadata: {labels: {app: writer}}
    spec:
      securityContext: {runAsUser: 1000, runAsGroup: 1000, fsGroup: 1000}
      containers:
        - name: writer
          image: busybox:1.37
          command: [sh, -c]
          args:
            - |
              cd /data
              mkdir -p app/nested cache
              echo hello > app/nested/hello.txt
              chmod 600 app/nested/hello.txt
              head -c 52428800 /dev/urandom > app/big.bin
              ln -sf nested/hello.txt app/link
              echo skip > cache/tmp.bin
              touch /tmp/ready
              sleep infinity
          readinessProbe: {exec: {command: [cat, /tmp/ready]}}
          volumeMounts: [{name: data, mountPath: /data}]
      volumes: [{name: data, persistentVolumeClaim: {claimName: data}}]
EOF
kubectl -n app rollout status deploy/writer --timeout=180s
EXPECTED=$(kubectl -n app exec deploy/writer -- sh -c 'cd /data/app && find . -type f -exec md5sum {} + | sort -k2 | md5sum')

log "Applying BackupConfig"
kubectl -n app apply -f - <<EOF
apiVersion: bardiup.io/v1alpha1
kind: BackupConfig
metadata: {name: e2e}
spec:
  schedule: "0 3 1 1 *"
  pvcSelector: {matchLabels: {backup: "yes"}}
  excludes: [cache]
  storage: $(s3_storage e2e)
  retention: {keepLast: 2}
EOF
wait_for app backupconfig/e2e '{.status.conditions[?(@.type=="Ready")].status}' True 60

for i in 1 2 3; do
  log "Backup run $i"
  kubectl -n app annotate backupconfig e2e bardiup.io/backup-now="run-$i" --overwrite
  wait_for app backupconfig/e2e '{.status.lastBackupNowTrigger}' "run-$i" 60
  latest=$(kubectl -n app get backups --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}')
  wait_for app "backup/$latest" '{.status.phase}' Completed 300
done

log "Checking retention"
sleep 3
kubectl -n app get backups -o wide
count=$(kubectl -n app get backups -o name | wc -l | tr -d ' ')
[[ "$count" == "2" ]] || fail "retention kept $count backups, want 2"
n=$(objects e2e)
[[ "$n" == "4" ]] || { aws s3 ls --recursive s3://backups/; fail "bucket holds $n objects, want 4 (2 archives + 2 manifests)"; }
retained=$(kubectl -n app get backups -o jsonpath='{.items[*].status.retainedBy[*]}')
[[ "$retained" == "last last" ]] || fail "retainedBy is '$retained'"
BACKUP=$(kubectl -n app get backups --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}')
KEY=$(kubectl -n app get backup "$BACKUP" -o jsonpath='{.status.objectKey}')
aws s3 cp "s3://backups/$KEY.json" -

log "Archive is a standard tarball"
aws s3 cp "s3://backups/$KEY" - >"$WORK/archive.tar.gz"
tar -tzvf "$WORK/archive.tar.gz" | head -20
tar -tzf "$WORK/archive.tar.gz" | grep -q '^cache' && fail "excluded path found in archive"
[[ "$(sha256sum "$WORK/archive.tar.gz" | cut -d' ' -f1)" == "$(kubectl -n app get backup "$BACKUP" -o jsonpath='{.status.sha256}')" ]] ||
  fail "archive checksum differs from status.sha256"

verify() { # verify <ns> <pvc>
  local got
  got=$(checksum_pod "$1" "$2" app)
  [[ "$got" == "$EXPECTED" ]] || fail "$1/$2: restored data differs"
  kubectl -n "$1" run "meta-$2" --rm -i --restart=Never --quiet --image=busybox:1.37 --overrides="{
    \"spec\":{\"containers\":[{\"name\":\"c\",\"image\":\"busybox:1.37\",\"command\":[\"sh\",\"-c\",\"test ! -e /data/cache && test -L /data/app/link && stat -c '%a %u %g' /data/app/nested/hello.txt\"],
    \"volumeMounts\":[{\"name\":\"d\",\"mountPath\":\"/data\"}]}],\"volumes\":[{\"name\":\"d\",\"persistentVolumeClaim\":{\"claimName\":\"$2\"}}]}}" >"$WORK/meta" 2>&1 ||
    { cat "$WORK/meta"; fail "$1/$2: excludes or symlink not preserved"; }
  grep -q '^600 1000 1000$' "$WORK/meta" || { cat "$WORK/meta"; fail "$1/$2: ownership/mode not preserved"; }
  echo "$1/$2: content, symlink, ownership and permissions match"
}

log "Restoring $BACKUP into a new PVC"
kubectl -n app apply -f - <<EOF
apiVersion: bardiup.io/v1alpha1
kind: Restore
metadata: {name: to-new-pvc}
spec: {backupName: $BACKUP, target: {pvcName: data-restored}}
EOF
wait_for app restore/to-new-pvc '{.status.phase}' Completed 300
verify app data-restored

log "Disaster recovery: restoring straight from the bucket into another namespace"
new_ns dr
kubectl -n dr apply -f - <<EOF
apiVersion: bardiup.io/v1alpha1
kind: Restore
metadata: {name: from-bucket}
spec:
  source: {objectKey: $KEY, storage: $(s3_storage unused)}
  target: {pvcName: recovered, size: 1Gi}
EOF
wait_for dr restore/from-bucket '{.status.phase}' Completed 300
verify dr recovered

if [[ -n "${E2E_SNAPSHOT:-}" ]]; then
  log "Snapshot method"
  kubectl -n app apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: csi-data}
spec: {storageClassName: csi-hostpath-sc, accessModes: [ReadWriteOnce], resources: {requests: {storage: 1Gi}}}
---
apiVersion: v1
kind: Pod
metadata: {name: csi-writer}
spec:
  containers:
    - name: w
      image: busybox:1.37
      command: [sh, -c, "echo snapshot-data > /data/file && sleep infinity"]
      volumeMounts: [{name: d, mountPath: /data}]
  volumes: [{name: d, persistentVolumeClaim: {claimName: csi-data}}]
EOF
  kubectl -n app wait --for=condition=Ready pod/csi-writer --timeout=180s
  kubectl -n app apply -f - <<EOF
apiVersion: bardiup.io/v1alpha1
kind: Backup
metadata: {name: snap}
spec:
  pvcName: csi-data
  method: snapshot
  snapshot: {volumeSnapshotClassName: csi-hostpath-snapclass}
  storage: $(s3_storage snap)
EOF
  wait_for app backup/snap '{.status.phase}' Completed 300
  sleep 3
  left=$(kubectl -n app get volumesnapshots,pvc -l app.kubernetes.io/managed-by=bardiup -o name)
  [[ -z "$left" ]] || fail "snapshot resources not cleaned up: $left"
  kubectl -n app apply -f - <<'EOF'
apiVersion: bardiup.io/v1alpha1
kind: Restore
metadata: {name: snap}
spec: {backupName: snap, target: {pvcName: snap-restored}}
EOF
  wait_for app restore/snap '{.status.phase}' Completed 300
  out=$(kubectl -n app run snap-check --rm -i --restart=Never --quiet --image=busybox:1.37 --overrides='{
    "spec":{"containers":[{"name":"c","image":"busybox:1.37","command":["cat","/data/file"],"volumeMounts":[{"name":"d","mountPath":"/data"}]}],
    "volumes":[{"name":"d","persistentVolumeClaim":{"claimName":"snap-restored"}}]}}' 2>&1)
  grep -q snapshot-data <<<"$out" || fail "snapshot restore content: $out"
  echo "snapshot backup and restore verified"
fi

log "Deleting a backup removes its data"
kubectl -n app delete backup "$BACKUP" --wait --timeout=60s
exists "$KEY" && fail "archive still in bucket after delete"
exists "$KEY.json" && fail "manifest still in bucket after delete"

log "Deleting the BackupConfig keeps remaining backups"
kubectl -n app delete backupconfig e2e --wait
[[ "$(kubectl -n app get backups -l bardiup.io/backup-config=e2e -o name | wc -l | tr -d ' ')" == "1" ]] ||
  fail "backups were removed with their BackupConfig"

footprint
log "All end-to-end checks passed"
