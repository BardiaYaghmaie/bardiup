# Contributing

Thanks for helping make Bardiup better. Bug reports, docs fixes and pull requests are all welcome.

## Reporting bugs and asking questions

Open an [issue](https://github.com/BardiaYaghmaie/bardiup/issues) with the Bardiup and Kubernetes versions, your storage (CSI driver, object store), and the output of:

```bash
kubectl describe backup <name>          # or backupconfig / restore
kubectl -n bardiup-system logs deploy/bardiup --tail=200
```

## Development

Requirements: Go (see `go.mod`), and for end-to-end tests Docker, [kind](https://kind.sigs.k8s.io), kubectl and Helm.

```bash
make test      # unit and integration tests, no cluster needed
make lint      # golangci-lint
make fuzz      # fuzz the archive extractor and the retention engine
make e2e       # full end-to-end test on a throwaway kind cluster
make stress    # parallel backups, controller kills, S3 outage (takes ~5 minutes)
```

The e2e and stress scripts create their own cluster and kubeconfig, so they never touch your current context. To run them against an existing test cluster, see the header of [hack/lib.sh](hack/lib.sh).

After changing types in `api/` or `+kubebuilder` markers, regenerate code, CRDs and RBAC:

```bash
make generate manifests
```

CI fails if generated files are out of date.

## Layout

| Path | Contents |
|---|---|
| `api/v1alpha1` | CRD types |
| `cmd/bardiup` | Entry point: controller manager, or `agent backup|restore` inside mover Jobs |
| `internal/controller` | BackupConfig, Backup and Restore reconcilers, Job construction, metrics |
| `internal/agent` | Data mover: archive + upload, download + extract, manifest |
| `internal/archive` | tar.gz writer and safe extractor |
| `internal/retention` | GFS retention engine (pure functions) |
| `internal/storage` | S3 client: multipart upload, resumable download |
| `charts/bardiup` | Helm chart; `crds/` and `files/role.yaml` are generated |
| `hack` | e2e and stress tests |

## Pull requests

- Keep changes focused, and add tests for behaviour changes.
- Run `make lint test` before pushing.
- Use [Conventional Commits](https://www.conventionalcommits.org/) for commit messages (`feat:`, `fix:`, `docs:` ...).
- Sign off your commits (`git commit -s`) to certify the [Developer Certificate of Origin](https://developercertificate.org/).
- Update `CHANGELOG.md` under an `Unreleased` heading for user-visible changes.

By contributing you agree that your contributions are licensed under the Apache License 2.0.
