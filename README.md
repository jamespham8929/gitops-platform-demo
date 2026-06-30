# gitops-platform-demo

A reference GitOps setup demonstrating a complete deployment pipeline from code commit to production. Uses GitHub Actions for CI, Helm for packaging, and ArgoCD for continuous delivery across staging and production clusters.

## Architecture

```
Developer push → GitHub Actions CI →  build + test + scan image
                                   →  push image to registry
                                   →  update Helm values with new tag
ArgoCD watches repo → detects values change → syncs to cluster
```

Environment promotion is gated: staging auto-syncs on every push to `main`, production requires a manual PR to bump the image tag in `charts/sample-service/values-production.yaml`.

## Repository structure

```
apps/
  sample-service/         Go HTTP service with /health and /ready endpoints
charts/
  sample-service/         Helm chart: Deployment, Service, HPA, Ingress
argocd/
  applications/           ArgoCD Application manifests per environment
.github/
  workflows/
    ci.yml                Build, test, lint, scan, push image
    promote.yml           Promote staging image tag to production chart values
```

## Prerequisites

- Kubernetes cluster with ArgoCD installed (`kubectl apply -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml`)
- `REGISTRY_USERNAME` and `REGISTRY_PASSWORD` secrets in GitHub repo settings
- `IMAGE_REGISTRY` variable set to your container registry (e.g., `ghcr.io/jamespham`)

## Deploying ArgoCD applications

```bash
kubectl apply -f argocd/applications/
```

ArgoCD will pick up the application definitions and begin syncing. Staging will auto-sync; production will show as `OutOfSync` until manually approved.

## Promoting to production

Each environment is pinned to an image tag in its own values file, and the two move independently. Staging tracks `main` automatically. Production only changes when a promotion PR is merged.

The flow for a single change:

1. Push to `main`. CI builds the image, tags it with the short commit SHA, and writes that tag into `charts/sample-service/values-staging.yaml`. ArgoCD auto-syncs staging.
2. Verify staging once ArgoCD reports the staging Application as `Synced` and `Healthy`.
3. Run the **Promote to production** workflow from the Actions tab and pass the staging image tag as its input. It opens a PR that bumps only the tag in `charts/sample-service/values-production.yaml`.

For example, promoting `a1b2c3d` (already live in staging) over the current production tag `9f8e7d6` produces this diff:

```diff
 image:
   # Updated by .github/workflows/promote.yml, which opens a PR for review.
-  tag: "9f8e7d6"
+  tag: "a1b2c3d"
```

4. Merge the PR. ArgoCD sees the new desired state and marks the production Application `OutOfSync`. An operator approves the sync in ArgoCD to roll it out, so rolling back is just reverting the merge.

## Local development

```bash
cd apps/sample-service
go run main.go

# Runs on :8080
curl http://localhost:8080/health
```

## Running CI locally

```bash
# Lint
golangci-lint run ./...

# Tests
go test ./... -v

# Build image
docker build -t sample-service:local apps/sample-service/
```

## License

MIT
