# Keel Installation

Keel automates Kubernetes deployment updates by tracking new container images.

## Directory Structure

```
keel/
├── values.yaml                # Custom Keel configuration
├── example-deployment.yaml    # Example deployment with Keel annotations
├── KEEL-CONFIGURATION.md     # Complete configuration guide
└── README.md                 # This file
```

## Quick Start

### Install Keel

```bash
# Add Helm repository (if not already added)
helm repo add keel https://charts.keel.sh
helm repo update

# Install Keel
helm install keel keel/keel \
  --namespace keel \
  --create-namespace \
  --values values.yaml
```

### Upgrade Keel

```bash
# Update to latest chart version
helm repo update

# Upgrade with current values
helm upgrade keel keel/keel \
  --namespace keel \
  --values values.yaml
```

### Uninstall Keel

```bash
helm uninstall keel --namespace keel
kubectl delete namespace keel
```

## Configuration

All customizations are in `values.yaml`:
- UI dashboard enabled (requires basic auth)
- Polling every 10 minutes
- Helm v3 provider enabled

### Access UI Dashboard

```bash
kubectl port-forward -n keel svc/keel 9300:9300
```

Then open: http://localhost:9300

**Login:** admin / admin

## Usage

See `KEEL-CONFIGURATION.md` for complete documentation on:
- Adding Keel annotations to deployments
- Policy types (force, semver, etc.)
- Tracking mutable tags like `latest-dev`
- Private registry authentication
- Monitoring and troubleshooting

## Verification

```bash
# Check Keel status
kubectl -n keel get pods

# View Keel logs
kubectl -n keel logs -l app=keel -f

# List current Helm releases
helm list -n keel
```

## Chart Information

- **Repository:** https://charts.keel.sh
- **Chart:** keel/keel
- **Documentation:** https://keel.sh/docs/
