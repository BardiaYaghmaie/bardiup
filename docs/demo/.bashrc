PS1='\[\e[34m\]~/shop\[\e[0m\] \[\e[35m\]❯\[\e[0m\] '
alias s3ls='kubectl -n s3 exec deploy/toolbox -- aws s3 ls --recursive --human-readable s3://backups/'
kubectl config set-context --current --namespace=shop >/dev/null
