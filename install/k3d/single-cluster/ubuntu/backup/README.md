# Backup Script

Creates a backup of your OpenChoreo cluster data including all planes and persistent volumes.

## Usage

```bash
./backup-cluster.sh
```

## What Gets Backed Up

- **Control Plane**: Projects, Organizations, Environments, all configurations
- **Data Plane**: Gateway configurations, workloads
- **Build Plane**: Argo Workflows, container registry data
- **Observability Plane**: OpenSearch logs, Prometheus metrics (all historical data)

## Options

```bash
# Full backup (default - backs up everything)
./backup-cluster.sh

# Backup to custom directory
./backup-cluster.sh --output-dir /path/to/backup

# Backup and compress
./backup-cluster.sh --compress

# Backup only observability plane (logs and metrics)
./backup-cluster.sh --observability-only

# Backup without persistent volumes (faster, configs only)
./backup-cluster.sh --skip-volumes
```

## Backup Location

Backups are saved to: `backups/backup-YYYY-MM-DD-HHMMSS/`

Example: `backups/backup-2024-11-25-143000/`

## Backup Structure

```
backup-2024-11-25-143000/
├── metadata.json              # Backup info
├── control-plane/            # Projects, Orgs, Environments
├── data-plane/               # Gateway configs, workloads
├── build-plane/              # Workflows, registry data
│   └── volumes/              # Container registry images
└── observability-plane/      # Logs and metrics
    └── volumes/
        ├── opensearch-*.tar.gz    # All logs
        └── prometheus-*.tar.gz    # All metrics
```

## Automated Backups

Add to crontab for daily backups:

```bash
crontab -e
```

Add this line for daily backup at 2 AM:

```
0 2 * * * /path/to/backup-cluster.sh --compress
```

## Important Notes

- Backups include **complete log history** from OpenSearch
- Backups include **complete metrics history** from Prometheus
- Backup size depends on how much log/metric data you have
- Store backups in a safe location
- Test your backups by restoring to a test cluster

## Restore

To restore from a backup, see [../restore/README.md](../restore/README.md)
