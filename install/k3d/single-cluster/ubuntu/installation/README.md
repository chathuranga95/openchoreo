# OpenChoreo Single-Cluster Installation for Ubuntu

Automated installation script for OpenChoreo on Ubuntu using k3d single-cluster setup.

## Quick Start

### Prerequisites

- Ubuntu 20.04+ (or Debian-based distribution)
- sudo access
- 8GB+ RAM (16GB recommended)
- 20GB+ free disk space

**Note**: Script will automatically install Docker, k3d, kubectl, and Helm if missing.

### Installation

```bash
cd openchoreo/install/k3d/single-cluster/ubuntu/installation
./install-single-cluster.sh
```

Wait 10-20 minutes. Installation will:
- Check and install prerequisites
- Create k3d cluster
- Install OpenChoreo (Control Plane, Data Plane, Build Plane)
- Verify installation

### Access URLs

After installation completes:

| Service | URL |
|---------|-----|
| OpenChoreo UI | http://openchoreo.localhost:8080 |
| OpenChoreo API | http://api.openchoreo.localhost:8080 |
| Asgardeo Thunder | http://thunder.openchoreo.localhost:8080 |
| User Workloads | http://localhost:9080 |
| Argo Workflows | http://localhost:10081 |
| Container Registry | http://localhost:10082 |

## Installation Options

```bash
# Without Build Plane (faster)
./install-single-cluster.sh --no-build-plane

# With Observability Plane
./install-single-cluster.sh --enable-observability

# With image preloading (recommended for slow networks)
./install-single-cluster.sh --preload-images

# Skip prerequisites (if already installed)
./install-single-cluster.sh --skip-prerequisites

# Show all options
./install-single-cluster.sh --help
```

## Uninstallation

```bash
# Remove OpenChoreo, keep cluster
./uninstall-single-cluster.sh

# Remove everything including cluster
./uninstall-single-cluster.sh --delete-cluster
```

## Quick Commands

```bash
# Check status
kubectl --context k3d-openchoreo get pods -A

# View DataPlane
kubectl --context k3d-openchoreo get dataplane -n default

# View BuildPlane
kubectl --context k3d-openchoreo get buildplane -n default

# Check detailed status
cd ../../../../../  # Go to repo root
./install/check-status.sh
```

## Common Issues

### Docker Permission Error

```bash
sudo usermod -aG docker $USER
newgrp docker  # Or log out and back in
```

### Pods Not Starting

```bash
# Check what's wrong
kubectl --context k3d-openchoreo get pods -A
kubectl --context k3d-openchoreo describe pod <pod-name> -n <namespace>

# View logs
kubectl --context k3d-openchoreo logs <pod-name> -n <namespace> --tail=50
```

### Colima Users

If using Colima and DNS doesn't work:

```bash
export K3D_FIX_DNS=0
./install-single-cluster.sh
```

### Out of Disk Space

```bash
docker system df
docker system prune -a
```

## Permission issues on helm installation directories
sudo chown -R $USER:$(id -gn) install/helm/*/charts/
chmod -R u+w install/helm/*/charts/

## Important Notes

1. **Docker Group**: After installation, if you see Docker permission errors, log out and back in (or run `newgrp docker`)

2. **System Resources**: Ensure you have sufficient RAM and disk space. Installation will fail if resources are insufficient.

3. **Network**: First installation requires internet access to download images. Use `--preload-images` for slow connections.

4. **Cluster Management**:
   ```bash
   # Stop cluster (preserves data)
   k3d cluster stop openchoreo
   
   # Start cluster
   k3d cluster start openchoreo
   
   # Delete cluster
   k3d cluster delete openchoreo
   ```

## What Gets Installed

- **k3d cluster**: Named "openchoreo" (context: `k3d-openchoreo`)
- **Control Plane**: Core OpenChoreo management components
- **Data Plane**: Runtime environment for applications
- **Build Plane**: CI/CD with Argo Workflows (optional, default: installed)
- **Observability Plane**: Logging and monitoring (optional, default: not installed)

## File Structure

```
ubuntu/installation/
├── install-single-cluster.sh      # Main installation script
├── uninstall-single-cluster.sh    # Uninstallation script
├── README.md                      # This file
└── [other documentation files]    # Detailed guides
```

Configuration files are in parent directory:
```
single-cluster/
├── config.yaml          # k3d cluster configuration
├── values-cp.yaml       # Control Plane values
├── values-dp.yaml       # Data Plane values
├── values-bp.yaml       # Build Plane values
└── values-op.yaml       # Observability Plane values
```

## Support

- **Script issues**: Check error messages in terminal output
- **Pod issues**: Use `kubectl describe pod` and `kubectl logs`
- **Status check**: Run `./install/check-status.sh` from repo root
- **Reinstall**: Run uninstall with `--delete-cluster`, then install again

## Quick Tips

1. **Alias for convenience**:
   ```bash
   alias k='kubectl --context k3d-openchoreo'
   ```

2. **Watch installation progress**:
   ```bash
   watch kubectl --context k3d-openchoreo get pods -A
   ```

3. **Save time on reinstalls**: Use `--preload-images` flag

4. **Clean environment**: Always use uninstall script before reinstalling

---

**That's all you need to know!** Run `./install-single-cluster.sh` and you're good to go. 🚀

