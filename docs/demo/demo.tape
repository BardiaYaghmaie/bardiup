# Re-record: run the "Demo GIF" workflow, or locally:
#   DEMO_KUBECONFIG=/tmp/demo-kubeconfig docs/demo/setup.sh && KUBECONFIG=/tmp/demo-kubeconfig vhs docs/demo/demo.tape
Output docs/demo.gif

Set Shell bash
Set FontSize 18
Set Width 1240
Set Height 720
Set Padding 26
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 55ms
Set CursorBlink false

Hide
Type "source docs/demo/.bashrc && cd docs/demo && clear" Enter
Sleep 1s
Show

Sleep 800ms
Type "cat nightly.yaml"
Sleep 400ms
Enter
Sleep 3.5s

Type "kubectl apply -f nightly.yaml && kubectl get bcfg"
Sleep 400ms
Enter
Sleep 3s

Type `kubectl annotate bcfg nightly bardiup.io/backup-now="$(date +%s)"`
Sleep 400ms
Enter
Sleep 1s
Type "kubectl get backups -w"
Sleep 300ms
Enter
Sleep 18s
Ctrl+C
Sleep 600ms

Type "s3ls"
Sleep 300ms
Enter
Sleep 3.5s

Type `kubectl create -f - <<< "{apiVersion: bardiup.io/v1alpha1, kind: Restore, metadata: {name: rollback}, spec: {backupName: $(kubectl get backups -o name | cut -d/ -f2), target: {pvcName: data-restored}}}"`
Sleep 400ms
Enter
Sleep 800ms
Type "kubectl get restore -w"
Sleep 300ms
Enter
Sleep 16s
Ctrl+C
Sleep 3s
