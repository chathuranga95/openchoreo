# Restore Script

Restores your OpenChoreo data after a fresh installation.

## Important: The Restore Workflow

Restore is designed for disaster recovery with this workflow:

1. **Backup** - Create backup of your data
2. **Delete** - Remove cluster completely
3. **Fresh Install** - Run `install-single-cluster.sh` (installs all infrastructure)
4. **Restore Data** - Run this script to restore your data

## What Gets Restored

✅ **Your Data:**
- Projects, Organizations, Environments
- User workloads and configurations
- OpenSearch logs (complete history)
- Prometheus metrics (complete history)

❌ **Not Restored (comes from fresh install):**
- CRDs, Helm charts, Infrastructure

## Usage

### Complete Disaster Recovery

```bash
# Step 1: Backup (do this regularly!)
cd ../backup
./backup-cluster.sh

# Step 2: Delete cluster
k3d cluster delete openchoreo

# Step 3: Fresh install
cd ../installation
./install-single-cluster.sh --enable-observability

# Step 4: Restore your data
cd ../restore
./restore-data-only.sh --backup-dir ../backup/backups/backup-2024-11-25-143000
```

### Options

```bash
# Basic restore (restores everything)
./restore-data-only.sh --backup-dir /path/to/backup

# Restore without volumes (faster, configs only, no logs/metrics)
./restore-data-only.sh --backup-dir /path/to/backup --skip-volumes

# Preview what will be restored (doesn't actually restore)
./restore-data-only.sh --backup-dir /path/to/backup --dry-run

# Force restore without confirmation (for automation)
./restore-data-only.sh --backup-dir /path/to/backup --force
```

## Verify Restoration

After restore completes:

```bash
# Check your Projects and Organizations are back
kubectl --context k3d-openchoreo get projects,organizations -A

# Check logs are restored (access OpenSearch Dashboard)
# http://localhost:11081

# Check all pods are running
kubectl --context k3d-openchoreo get pods -A
```

## What Happens During Restore

1. Validates backup and cluster
2. Restores Projects, Organizations, Environments
3. Restores user workloads
4. Scales down observability components
5. Restores OpenSearch data (logs)
6. Restores Prometheus data (metrics)
7. Scales back up observability components
8. Verifies restoration

## Important Notes

- **Always run fresh install first** before restoring data
- Your **logs and metrics** are fully preserved with complete history
- Restore time depends on how much data you have (volumes)
- Use `--skip-volumes` for faster restore if you don't need logs/metrics history
- The script safely handles persistent volumes by scaling pods down/up

## Troubleshooting

### "Namespace not found" error
- Make sure you ran fresh install first: `install-single-cluster.sh`

### Pods not starting after restore
```bash
# Check pod status
kubectl --context k3d-openchoreo get pods -A

# Check specific pod
kubectl --context k3d-openchoreo describe pod <pod-name> -n <namespace>
```

### Restore takes too long
- This is normal if you have a lot of logs/metrics data
- Use `--skip-volumes` if you don't need historical data
