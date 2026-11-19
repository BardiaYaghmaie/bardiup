#!/usr/bin/env bash
# Stress and chaos test: parallel backups across tenants, controller restarts, an object storage
# outage and parallel restores. See hack/lib.sh for running against an existing cluster.
# shellcheck source=hack/lib.sh
source "$(dirname "$0")/lib.sh"

PVCS=8
TENANTS=2
T0=$SECONDS

setup_cluster 2
deploy_s3
install_bardiup

backupconfig() { # backupconfig <ns> <name> <schedule> <selector-yaml> <retention-yaml>
  kubectl -n "$1" apply -f - <<EOF
apiVersion: bardiup.io/v1alpha1
kind: BackupConfig
metadata: {name: $2}
spec:
  schedule: "$3"
  pvcSelector: $4
  storage: $(s3_storage "$1-$2")
  retention: $5
EOF
}

statefulset() { # statefulset <ns> <name> <replicas> <size> <script>
  kubectl -n "$1" apply -f - <<EOF
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: $2}
spec:
  replicas: $3
  podManagementPolicy: Parallel
  selector: {matchLabels: {app: $2}}
  template:
    metadata: {labels: {app: $2}}
    spec:
      terminationGracePeriodSeconds: 1
      containers:
        - name: w
          image: busybox:1.37
          command: [sh, -c, "$5; touch /tmp/ready; sleep infinity"]
          readinessProbe: {exec: {command: [cat, /tmp/ready]}, periodSeconds: 2}
          resources: {requests: {cpu: 5m, memory: 8Mi}}
          volumeMounts: [{name: data, mountPath: /data}]
  volumeClaimTemplates:
    - metadata: {name: data, labels: {set: $2}}
      spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: $4}}}
EOF
}

# ---------------------------------------------------------------------------
log "1. Fan-out: $PVCS PVCs in one namespace + $TENANTS tenant namespaces, all at once"
new_ns fanout
# shellcheck disable=SC2016 # expanded inside the pod
statefulset fanout db "$PVCS" 1Gi 'mkdir -p /data/d && for i in $(seq 1 200); do echo $HOSTNAME-$i > /data/d/f$i; done; head -c 5242880 /dev/urandom > /data/blob'
for t in $(seq 1 "$TENANTS"); do
  new_ns "tenant-$t"
  statefulset "tenant-$t" app 2 1Gi 'echo tenant > /data/f; head -c 1048576 /dev/urandom > /data/blob'
done
kubectl -n fanout rollout status sts/db --timeout=600s
for t in $(seq 1 "$TENANTS"); do kubectl -n "tenant-$t" rollout status sts/app --timeout=300s; done

backupconfig fanout all "0 3 1 1 *" "{matchLabels: {set: db}}" "{keepLast: 3}"
for t in $(seq 1 "$TENANTS"); do backupconfig "tenant-$t" nightly "0 3 1 1 *" "{}" "{daily: 7}"; done

start=$SECONDS
kubectl -n fanout annotate backupconfig all bardiup.io/backup-now=1 --overwrite
for t in $(seq 1 "$TENANTS"); do kubectl -n "tenant-$t" annotate backupconfig nightly bardiup.io/backup-now=1 --overwrite; done
wait_all fanout backups Completed "$PVCS" 900
for t in $(seq 1 "$TENANTS"); do wait_all "tenant-$t" backups Completed 2 300; done
echo "$((PVCS + TENANTS * 2)) backups completed in $((SECONDS - start))s"
kubectl -n fanout get backups --no-headers | head -5
footprint

# ---------------------------------------------------------------------------
log "2. Chaos: controller killed while backups run"
kubectl -n fanout annotate backupconfig all bardiup.io/backup-now=2 --overwrite
wait_for fanout backupconfig/all '{.status.lastBackupNowTrigger}' 2 60
sleep 2
kubectl -n bardiup-system delete pod -l app.kubernetes.io/name=bardiup --wait=false
sleep 5
kubectl -n bardiup-system delete pod -l app.kubernetes.io/name=bardiup --wait=false
kubectl -n bardiup-system rollout status deploy/bardiup --timeout=180s
wait_all fanout backups Completed $((PVCS * 2)) 900
dups=$(kubectl -n fanout get backups -o jsonpath='{range .items[*]}{.spec.pvcName}{"\n"}{end}' | sort | uniq -c | awk '$1 != 2' | wc -l | tr -d ' ')
[[ "$dups" == "0" ]] || fail "controller restarts produced missing or duplicate backups"
echo "all $((PVCS * 2)) backups completed exactly once across two controller restarts"

# ---------------------------------------------------------------------------
log "3. Object storage outage and recovery"
kubectl -n s3 scale deploy/rustfs --replicas=0
kubectl -n s3 wait --for=delete pod -l app=rustfs --timeout=120s
kubectl -n tenant-1 annotate backupconfig nightly bardiup.io/backup-now=outage --overwrite
wait_all tenant-1 backups Failed 2 900
kubectl -n tenant-1 get backups -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,MESSAGE:.status.message | head -5
wait_for tenant-1 backupconfig/nightly '{.status.conditions[?(@.type=="LastBackupSucceeded")].status}' False 60
kubectl -n s3 scale deploy/rustfs --replicas=1
kubectl -n s3 rollout status deploy/rustfs --timeout=180s
kubectl -n tenant-1 annotate backupconfig nightly bardiup.io/backup-now=recovered --overwrite
# daily: 7 keeps one backup per PVC per day, so recovery shows up as the condition flipping back.
wait_for tenant-1 backupconfig/nightly '{.status.conditions[?(@.type=="LastBackupSucceeded")].status}' True 600
kubectl -n tenant-1 get backups
echo "failures were reported and the next run recovered"

# ---------------------------------------------------------------------------
log "4. Concurrent restores: $PVCS at once"
for b in $(kubectl -n fanout get backups -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.pvcName}{"\n"}{end}' | sort -k2 -u | awk '{print $1}'); do
  echo "{apiVersion: bardiup.io/v1alpha1, kind: Restore, metadata: {name: r-$b}, spec: {backupName: $b, target: {pvcName: r-$b}}}"
  echo "---"
done | kubectl -n fanout apply -f - >/dev/null
start=$SECONDS
wait_all fanout restores Completed "$PVCS" 900
echo "$PVCS restores completed in $((SECONDS - start))s"
sample=$(kubectl -n fanout get restores -o jsonpath='{.items[0].spec.target.pvcName}')
src=$(kubectl -n fanout get backup "${sample#r-}" -o jsonpath='{.spec.pvcName}')
want=$(kubectl -n fanout exec "${src#data-}" -- sh -c 'cd /data/d && find . -type f -exec md5sum {} + | sort -k2 | md5sum')
[[ "$(checksum_pod fanout "$sample" d)" == "$want" ]] || fail "concurrent restore $sample differs from $src"

footprint
log "Stress test passed in $(((SECONDS - T0) / 60))m"
