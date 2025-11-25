#!/usr/bin/env bash

# OpenChoreo k3d Cluster Deletion
# Simply deletes the k3d cluster (removes everything)

set -e

CLUSTER_NAME="${CLUSTER_NAME:-openchoreo}"

echo "🗑️  Deleting k3d cluster: ${CLUSTER_NAME}"
echo ""
echo "This will remove:"
echo "  • The entire cluster"
echo "  • All OpenChoreo components"
echo "  • All data (unless backed up)"
echo ""

read -p "Continue? (y/N): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Cancelled."
    exit 0
fi

echo ""
echo "Deleting cluster..."
k3d cluster delete "${CLUSTER_NAME}"

echo ""
echo "✅ Cluster deleted successfully!"
echo ""
echo "To reinstall:"
echo "  ./install-single-cluster.sh"
echo ""
