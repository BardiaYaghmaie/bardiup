# Re-record: run the "Demo GIF" workflow, or locally:
#   DEMO_KUBECONFIG=/tmp/demo-kubeconfig docs/demo/setup.sh && KUBECONFIG=/tmp/demo-kubeconfig vhs docs/demo/demo.tape
Output docs/demo.gif

Set Shell bash
Set FontSize 17
Set Width 1500
Set Height 860
Set Padding 26
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 50ms
Set CursorBlink false

Hide
Type "source docs/demo/.bashrc && cd docs/demo && clear" Enter
Sleep 1s
Show

# 1. one small config: schedule, which PVCs, where, how long
Sleep 700ms
Type "cat nightly.yaml"
Enter
Sleep 4s
Type "kubectl apply -f nightly.yaml"
Enter
Sleep 3s
Type "kubectl get backupconfig"
Enter
Sleep 3s

# 2. back up now instead of waiting for 02:00
Type "clear" Enter
Type `kubectl annotate backupconfig nightly bardiup.io/backup-now="$(date +%s)"`
Enter
Sleep 800ms
Type "kubectl get backups -w"
Enter
Sleep 16s
Ctrl+C
Sleep 1.5s

# 3. it's a plain tar.gz in your bucket
Type "s3 ls --recursive --human-readable s3://backups/"
Enter
Sleep 3s
Type "s3 cp s3://backups/$(s3 ls --recursive s3://backups/ | awk '/gz$/{print $4}') - | tar -tzv | head -4"
Enter
Sleep 5s

# 4. restore into a new PVC
Type "clear" Enter
Hide
Type `printf 'apiVersion: bardiup.io/v1alpha1\nkind: Restore\nmetadata:\n  name: rollback\nspec:\n  backupName: %s\n  target:\n    pvcName: data-restored\n' "$(kubectl get backups -o jsonpath='{.items[0].metadata.name}')" > restore.yaml && clear`
Enter
Sleep 500ms
Show
Type "cat restore.yaml"
Enter
Sleep 3s
Type "kubectl apply -f restore.yaml && kubectl get restore -w"
Enter
Sleep 14s
Ctrl+C
Sleep 800ms
Type "kubectl get pvc data-restored"
Enter
Sleep 4s
