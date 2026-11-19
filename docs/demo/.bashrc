PS1='\[\e[34m\]~/shop\[\e[0m\] \[\e[35m\]❯\[\e[0m\] '
kubectl config set-context --current --namespace=shop >/dev/null
s3() { kubectl -n s3 exec -i deploy/toolbox -- aws s3 "$@"; }
