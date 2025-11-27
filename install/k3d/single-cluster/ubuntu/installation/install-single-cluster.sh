#!/usr/bin/env bash

# OpenChoreo Single-Cluster Installation Script for Ubuntu
# This script automates the complete installation process including prerequisites

set -eo pipefail

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
RESET='\033[0m'

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../../.." && pwd)"
SINGLE_CLUSTER_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Configuration
CLUSTER_NAME="openchoreo"
KUBE_CONTEXT="k3d-${CLUSTER_NAME}"
RECREATE_CLUSTER="${RECREATE_CLUSTER:-false}"
INSTALL_BUILD_PLANE="${INSTALL_BUILD_PLANE:-true}"
INSTALL_OBSERVABILITY="${INSTALL_OBSERVABILITY:-true}"
SKIP_PREREQUISITES="${SKIP_PREREQUISITES:-true}"
PRELOAD_IMAGES="${PRELOAD_IMAGES:-false}"
USE_UBUNTU_DOCKER="${USE_UBUNTU_DOCKER:-true}"  # Use Ubuntu's docker.io by default

# Version requirements
MIN_DOCKER_VERSION="20.10"
MIN_K3D_VERSION="5.8.0"
MIN_KUBECTL_VERSION="1.32.0"
MIN_HELM_VERSION="3.12.0"

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${RESET} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${RESET} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${RESET} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${RESET} $1"
}

log_step() {
    echo -e "\n${CYAN}${BOLD}==>${RESET} ${BOLD}$1${RESET}\n"
}

# Check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Compare version numbers
version_ge() {
    [ "$(printf '%s\n' "$1" "$2" | sort -V | head -n1)" = "$2" ]
}

check_install_jq() {
    log_step "Checking jq installation..."

    if command_exists jq; then
        JQ_VERSION=$(jq --version 2>/dev/null)
        log_success "jq is already installed ($JQ_VERSION)"
    else
        log_warning "jq not found. Installing jq..."
        sudo apt-get update
        if sudo apt-get install -y jq; then
            log_success "jq installed successfully"
        else
            log_error "Failed to install jq. Please install jq manually."
            exit 1
        fi
    fi
}

# Check and install Docker
check_install_docker() {
    log_step "Checking Docker installation..."
    
    if command_exists docker; then
        DOCKER_VERSION=$(docker --version | grep -oP '\d+\.\d+\.\d+' | head -n1)
        log_info "Docker version: $DOCKER_VERSION"
        
        if version_ge "$DOCKER_VERSION" "$MIN_DOCKER_VERSION"; then
            log_success "Docker version meets requirements (>= $MIN_DOCKER_VERSION)"
        else
            log_warning "Docker version $DOCKER_VERSION is below recommended $MIN_DOCKER_VERSION"
        fi
        
        # Check if Docker daemon is running
        if ! docker ps >/dev/null 2>&1; then
            log_error "Docker daemon is not running"
            log_info "Starting Docker service..."
            sudo systemctl start docker
            sudo systemctl enable docker
            sleep 5
        fi
        
        # Ensure docker group exists and user is in it
        if ! getent group docker >/dev/null; then
            log_info "Creating docker group..."
            sudo groupadd docker
        fi
        
        if ! groups | grep -q docker; then
            log_warning "Current user is not in docker group"
            log_info "Adding current user to docker group..."
            sudo usermod -aG docker "$USER"
            log_warning "You need to log out and log back in for group changes to take effect"
            log_warning "Or run: newgrp docker"
        fi
        
        log_success "Docker is ready"
    else
        log_warning "Docker not found. Installing Docker..."
        
        sudo apt-get update
        
        # Try Ubuntu repository first if enabled (simpler, no SSL issues)
        if [[ "$USE_UBUNTU_DOCKER" == "true" ]]; then
            log_info "Installing Docker from Ubuntu repository (docker.io)..."
            
            if sudo DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose; then
                log_success "Docker installed from Ubuntu repository"
                
                # Ensure docker group exists
                if ! getent group docker >/dev/null; then
                    log_info "Creating docker group..."
                    sudo groupadd docker
                fi
                
                # Add current user to docker group
                log_info "Adding current user to docker group..."
                sudo usermod -aG docker "$USER"
                
                # Start and enable Docker
                log_info "Starting Docker service..."
                sudo systemctl start docker
                sudo systemctl enable docker
                
                log_success "Docker installed successfully"
                log_warning "You need to log out and log back in for group changes to take effect"
                log_warning "Or run: newgrp docker"
                return 0
            else
                log_warning "Failed to install from Ubuntu repository, trying official Docker repository..."
            fi
        fi
        
        # Install from official Docker repository
        log_info "Installing Docker from official Docker repository..."
            
            # Install prerequisites
            sudo apt-get install -y \
                ca-certificates \
                curl \
                gnupg \
                lsb-release
            
            # Update CA certificates
            sudo update-ca-certificates --fresh
            
            # Add Docker's official GPG key
            sudo install -m 0755 -d /etc/apt/keyrings
            
            if curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg 2>/dev/null; then
                sudo chmod a+r /etc/apt/keyrings/docker.gpg
            else
                log_error "Failed to download Docker GPG key. Please check your internet connection and SSL certificates."
                log_info "You can try manually installing Docker with: sudo apt install docker.io"
                return 1
            fi
            
            # Set up Docker repository
            echo \
              "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \
              $(lsb_release -cs) stable" | \
              sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
            
            # Install Docker Engine
            sudo apt-get update
            sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
        
        # Ensure docker group exists
        if ! getent group docker >/dev/null; then
            log_info "Creating docker group..."
            sudo groupadd docker
        fi
        
        # Add current user to docker group
        log_info "Adding current user to docker group..."
        sudo usermod -aG docker "$USER"
        
        # Start and enable Docker
        log_info "Starting Docker service..."
        sudo systemctl start docker
        sudo systemctl enable docker
        
        log_success "Docker installed successfully"
        log_warning "You need to log out and log back in for group changes to take effect"
        log_warning "Or run: newgrp docker"
    fi
}

# Check and install k3d
check_install_k3d() {
    log_step "Checking k3d installation..."
    
    if command_exists k3d; then
        K3D_VERSION=$(k3d version | grep -oP 'k3d version v\K[\d.]+' | head -n1)
        log_info "k3d version: $K3D_VERSION"
        
        if version_ge "$K3D_VERSION" "$MIN_K3D_VERSION"; then
            log_success "k3d version meets requirements (>= $MIN_K3D_VERSION)"
        else
            log_warning "k3d version $K3D_VERSION is below recommended $MIN_K3D_VERSION"
            log_info "Upgrading k3d..."
            curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash
        fi
    else
        log_warning "k3d not found. Installing k3d..."
        curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash
        log_success "k3d installed successfully"
    fi
}

# Check and install kubectl
check_install_kubectl() {
    log_step "Checking kubectl installation..."
    
    if command_exists kubectl; then
        KUBECTL_VERSION=$(kubectl version --client -o json 2>/dev/null | grep -oP '"gitVersion": "v\K[\d.]+' | head -n1)
        log_info "kubectl version: $KUBECTL_VERSION"
        
        if version_ge "$KUBECTL_VERSION" "$MIN_KUBECTL_VERSION"; then
            log_success "kubectl version meets requirements (>= $MIN_KUBECTL_VERSION)"
        else
            log_warning "kubectl version $KUBECTL_VERSION is below recommended $MIN_KUBECTL_VERSION"
        fi
    else
        log_warning "kubectl not found. Installing kubectl..."
        
        # Download kubectl
        KUBECTL_URL="https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
        curl -LO "$KUBECTL_URL"
        
        # Install kubectl
        sudo install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl
        rm kubectl
        
        log_success "kubectl installed successfully"
    fi
}

# Check and install Helm
check_install_helm() {
    log_step "Checking Helm installation..."
    
    if command_exists helm; then
        HELM_VERSION=$(helm version --template='{{.Version}}' | grep -oP 'v\K[\d.]+')
        log_info "Helm version: $HELM_VERSION"
        
        if version_ge "$HELM_VERSION" "$MIN_HELM_VERSION"; then
            log_success "Helm version meets requirements (>= $MIN_HELM_VERSION)"
        else
            log_warning "Helm version $HELM_VERSION is below recommended $MIN_HELM_VERSION"
            log_info "Upgrading Helm..."
            curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
        fi
    else
        log_warning "Helm not found. Installing Helm..."
        curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
        log_success "Helm installed successfully"
    fi
}

# Create k3d cluster
create_cluster() {
    log_step "Creating k3d cluster..."
    
    # Check if cluster already exists
    if k3d cluster list 2>/dev/null | grep -q "^${CLUSTER_NAME}"; then
        log_warning "Cluster '${CLUSTER_NAME}' already exists"
        
        # Check RECREATE_CLUSTER environment variable
        if [[ "${RECREATE_CLUSTER:-false}" =~ ^(true|yes|y|1)$ ]]; then
            log_info "RECREATE_CLUSTER is set. Deleting existing cluster..."
            k3d cluster delete "${CLUSTER_NAME}"
        else
            log_info "Using existing cluster (set RECREATE_CLUSTER=true to recreate)"
            return 0
        fi
    fi
    
    # Check for Colima and set K3D_FIX_DNS if needed
    if command_exists colima && colima status >/dev/null 2>&1; then
        log_warning "Colima detected. Setting K3D_FIX_DNS=0 to avoid DNS issues"
        export K3D_FIX_DNS=0
    fi
    
    # Create cluster
    log_info "Creating cluster with config: ${SINGLE_CLUSTER_DIR}/config.yaml"
    if k3d cluster create --config "${SINGLE_CLUSTER_DIR}/config.yaml"; then
        log_success "Cluster created successfully"
    else
        log_error "Failed to create cluster"
        return 1
    fi
    
    # Wait for cluster to be ready
    log_info "Waiting for cluster to be ready..."
    sleep 10
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready nodes --all --timeout=300s
    
    log_success "Cluster is ready"
}

# Preload images (optional)
preload_images() {
    log_step "Preloading images into cluster..."
    
    local preload_script="${REPO_ROOT}/install/k3d/preload-images.sh"
    if [[ ! -f "$preload_script" ]]; then
        log_warning "Preload script not found at $preload_script, skipping"
        return 0
    fi
    
    local args=(
        "--cluster" "${CLUSTER_NAME}"
        "--local-charts"
        "--control-plane" "--cp-values" "${SCRIPT_DIR}/values-cp.yaml"
        "--data-plane" "--dp-values" "${SINGLE_CLUSTER_DIR}/values-dp.yaml"
        "--parallel" "4"
    )
    
    if [[ "$INSTALL_BUILD_PLANE" == "true" ]]; then
        args+=("--build-plane" "--bp-values" "${SINGLE_CLUSTER_DIR}/values-bp.yaml")
    fi
    
    if [[ "$INSTALL_OBSERVABILITY" == "true" ]]; then
        args+=("--observability-plane" "--op-values" "${SINGLE_CLUSTER_DIR}/values-op.yaml")
    fi
    
    log_info "Running: ${preload_script} ${args[*]}"
    if bash "$preload_script" "${args[@]}"; then
        log_success "Images preloaded successfully"
    else
        log_warning "Image preloading failed, continuing anyway"
    fi
}

# Install Helm chart
install_helm_chart() {
    local release_name="$1"
    local chart_path="$2"
    local namespace="$3"
    local values_file="$4"
    
    log_info "Installing ${release_name} in namespace ${namespace}..."
    
    local helm_args=(
        "install" "${release_name}" "${chart_path}"
        "--dependency-update"
        "--kube-context" "${KUBE_CONTEXT}"
        "--namespace" "${namespace}"
        "--create-namespace"
        "--timeout" "30m"
    )
    
    if [[ -n "$values_file" && -f "$values_file" ]]; then
        helm_args+=("--values" "$values_file")
    fi
    
    if helm "${helm_args[@]}"; then
        log_success "${release_name} installed successfully"
    else
        log_error "Failed to install ${release_name}"
        return 1
    fi
}

# Wait for CRD to be available
wait_for_crd() {
    local crd_name="$1"
    local timeout="${2:-60}"
    local interval="${3:-2}"
    local elapsed=0
    
    log_info "Waiting for CRD '${crd_name}' to be available..."
    
    while [ $elapsed -lt $timeout ]; do
        if kubectl --context="${KUBE_CONTEXT}" get crd "${crd_name}" >/dev/null 2>&1; then
            log_success "CRD '${crd_name}' is available"
            return 0
        fi
        sleep $interval
        elapsed=$((elapsed + interval))
        log_info "Waiting for CRD '${crd_name}'... (${elapsed}s/${timeout}s)"
    done
    
    log_error "CRD '${crd_name}' did not become available within ${timeout} seconds"
    return 1
}

create_ingress_middleware() {
    log_step "Creating ingress middleware..."
    
    # Create the namespace if it does not exist
    kubectl --context="${KUBE_CONTEXT}" get namespace openchoreo-control-plane >/dev/null 2>&1 || \
        kubectl --context="${KUBE_CONTEXT}" create namespace openchoreo-control-plane

    # Wait for Traefik Middleware CRD to be available
    if ! wait_for_crd "middlewares.traefik.io" 60; then
        log_error "Traefik Middleware CRD is not available. Traefik may not be properly installed."
        return 1
    fi
    
    # Apply the middleware
    log_info "Applying ingress middleware..."
    if kubectl --context="${KUBE_CONTEXT}" apply -f "${SCRIPT_DIR}/ingress-middleware.yaml"; then
        log_success "Ingress middleware created successfully"
    else
        log_error "Failed to create ingress middleware"
        return 1
    fi
}

# Install Control Plane
install_control_plane() {
    create_ingress_middleware

    log_step "Installing OpenChoreo Control Plane..."
    
    install_helm_chart \
        "openchoreo-control-plane" \
        "${REPO_ROOT}/install/helm/openchoreo-control-plane" \
        "openchoreo-control-plane" \
        "${SCRIPT_DIR}/values-cp.yaml"
    
    log_info "Waiting for Control Plane pods to be ready..."
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready pods \
        -n openchoreo-control-plane \
        -l app.kubernetes.io/name=openchoreo-control-plane \
        --timeout=600s || log_warning "Some Control Plane pods may not be ready yet"
}

# Install Data Plane
install_data_plane() {
    log_step "Installing OpenChoreo Data Plane..."
    
    install_helm_chart \
        "openchoreo-data-plane" \
        "${REPO_ROOT}/install/helm/openchoreo-data-plane" \
        "openchoreo-data-plane" \
        "${SINGLE_CLUSTER_DIR}/values-dp.yaml"
    
    log_info "Waiting for Data Plane pods to be ready..."
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready pods \
        -n openchoreo-data-plane \
        -l app.kubernetes.io/name=gateway-helm \
        --timeout=600s || log_warning "Some Data Plane pods may not be ready yet"
}

# Install Build Plane
install_build_plane() {
    log_step "Installing OpenChoreo Build Plane..."
    
    install_helm_chart \
        "openchoreo-build-plane" \
        "${REPO_ROOT}/install/helm/openchoreo-build-plane" \
        "openchoreo-build-plane" \
        "${SINGLE_CLUSTER_DIR}/values-bp.yaml"
    
    log_info "Waiting for Build Plane pods to be ready..."
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready pods \
        -n openchoreo-build-plane \
        -l app.kubernetes.io/name=argo \
        --timeout=600s || log_warning "Some Build Plane pods may not be ready yet"
}

# Install Observability Plane
install_observability_plane() {
    log_step "Installing OpenChoreo Observability Plane..."

    log_info "Installing opensearch operator"
    helm repo add opensearch-operator https://opensearch-project.github.io/opensearch-k8s-operator/
    helm repo update
    helm install opensearch-operator opensearch-operator/opensearch-operator --create-namespace -n openchoreo-observability-plane

    install_helm_chart \
        "openchoreo-observability-plane" \
        "${REPO_ROOT}/install/helm/openchoreo-observability-plane" \
        "openchoreo-observability-plane" \
        "${SINGLE_CLUSTER_DIR}/values-op.yaml"

    log_info "Waiting for Observability Plane pods to be ready..."
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready pods \
        -n openchoreo-observability-plane \
        -l app.kubernetes.io/component=observer \
        --timeout=600s || log_warning "Some Observability Plane pods may not be ready yet"
    
    log_step "Configuring Dataplane and Buildplane with the Observability plane"

    # Configure DataPlane to use observer service
    kubectl patch dataplane default -n default --type merge -p '{"spec":{"observer":{"url":"http://observer.openchoreo-observability-plane:8080","authentication":{"basicAuth":{"username":"dummy","password":"dummy"}}}}}'

    # Configure BuildPlane to use observer service
    kubectl patch buildplane default -n default --type merge -p '{"spec":{"observer":{"url":"http://observer.openchoreo-observability-plane:8080","authentication":{"basicAuth":{"username":"dummy","password":"dummy"}}}}}'
}

# Install Keel
install_keel() {
    log_step "Installing Keel for automatic deployment updates..."

    # Check if Keel is already installed
    if helm --kube-context="${KUBE_CONTEXT}" list -n keel 2>/dev/null | grep -q "^keel"; then
        log_info "Keel is already installed"

        # Check if we should upgrade
        local keel_values="${SINGLE_CLUSTER_DIR}/keel/values.yaml"
        if [[ -f "$keel_values" ]]; then
            log_info "Upgrading Keel with latest configuration..."
            if helm --kube-context="${KUBE_CONTEXT}" upgrade keel keel/keel \
                --namespace keel \
                --values "$keel_values" \
                --timeout 5m; then
                log_success "Keel upgraded successfully"
            else
                log_warning "Failed to upgrade Keel, but continuing with existing installation"
            fi
        fi
        return 0
    fi

    # Add Keel Helm repository
    log_info "Adding Keel Helm repository..."
    if helm repo add keel https://charts.keel.sh 2>/dev/null; then
        log_success "Keel repository added"
    else
        log_info "Keel repository already exists"
    fi

    helm repo update >/dev/null 2>&1

    # Check for values file
    local keel_values="${SINGLE_CLUSTER_DIR}/keel/values.yaml"
    if [[ ! -f "$keel_values" ]]; then
        log_warning "Keel values file not found at $keel_values"
        log_warning "Skipping Keel installation"
        return 0
    fi

    # Install Keel
    log_info "Installing Keel from keel/keel chart..."
    local helm_args=(
        "install" "keel" "keel/keel"
        "--kube-context" "${KUBE_CONTEXT}"
        "--namespace" "keel"
        "--create-namespace"
        "--values" "$keel_values"
        "--timeout" "5m"
    )

    if helm "${helm_args[@]}"; then
        log_success "Keel installed successfully"
    else
        log_error "Failed to install Keel"
        return 1
    fi

    # Wait for Keel to be ready
    log_info "Waiting for Keel to be ready..."
    kubectl --context="${KUBE_CONTEXT}" wait --for=condition=Ready pods \
        -n keel \
        -l app=keel \
        --timeout=300s || log_warning "Keel pod may not be ready yet"

    log_success "Keel is ready"
}

# Create DataPlane resource
create_dataplane_resource() {
    log_step "Creating DataPlane resource..."
    
    local script="${REPO_ROOT}/install/add-data-plane.sh"
    if [[ ! -f "$script" ]]; then
        log_error "Script not found: $script"
        return 1
    fi
    
    # Create temporary copy and replace publicVirtualHost value
    local temp_script
    temp_script=$(mktemp)
    sed 's/publicVirtualHost: openchoreoapis\.localhost/publicVirtualHost: openchoreoapis.upstream/g' "$script" > "$temp_script"
    chmod +x "$temp_script"
    
    # Execute the modified script
    if bash "$temp_script" --control-plane-context "${KUBE_CONTEXT}"; then
        log_success "DataPlane resource created successfully"
        rm -f "$temp_script"
    else
        log_error "Failed to create DataPlane resource"
        rm -f "$temp_script"
        return 1
    fi
}

# Create BuildPlane resource
create_buildplane_resource() {
    log_step "Creating BuildPlane resource..."
    
    local script="${REPO_ROOT}/install/add-build-plane.sh"
    if [[ ! -f "$script" ]]; then
        log_error "Script not found: $script"
        return 1
    fi
    
    if bash "$script" --control-plane-context "${KUBE_CONTEXT}"; then
        log_success "BuildPlane resource created successfully"
    else
        log_error "Failed to create BuildPlane resource"
        return 1
    fi
}

# Verify installation
verify_installation() {
    log_step "Verifying installation..."
    
    local script="${REPO_ROOT}/install/check-status.sh"
    if [[ -f "$script" ]]; then
        bash "$script"
    else
        log_warning "Status check script not found, performing basic verification"
        
        # Basic verification
        log_info "Checking pods in openchoreo-control-plane namespace..."
        kubectl --context="${KUBE_CONTEXT}" get pods -n openchoreo-control-plane
        
        log_info "Checking pods in openchoreo-data-plane namespace..."
        kubectl --context="${KUBE_CONTEXT}" get pods -n openchoreo-data-plane
        
        if [[ "$INSTALL_BUILD_PLANE" == "true" ]]; then
            log_info "Checking pods in openchoreo-build-plane namespace..."
            kubectl --context="${KUBE_CONTEXT}" get pods -n openchoreo-build-plane
        fi
        
        if [[ "$INSTALL_OBSERVABILITY" == "true" ]]; then
            log_info "Checking pods in openchoreo-observability-plane namespace..."
            kubectl --context="${KUBE_CONTEXT}" get pods -n openchoreo-observability-plane
        fi
    fi
}

# Print access information
print_access_info() {
    log_step "Installation Complete!"
    
    echo -e "${GREEN}${BOLD}OpenChoreo has been installed successfully!${RESET}\n"
    
    echo -e "${CYAN}${BOLD}Access Information:${RESET}\n"
    
    echo -e "${BOLD}Control Plane:${RESET}"
    echo -e "  - OpenChoreo UI:      ${BLUE}http://hostedopenchoreo.localhost:8080${RESET}"
    echo -e "  - OpenChoreo API:     ${BLUE}http://api.hostedopenchoreo.localhost:8080${RESET}"
    echo -e "  - Asgardeo Thunder:   ${BLUE}http://thunder.hostedopenchoreo.localhost:8080${RESET}"
    
    echo -e "\n${BOLD}Data Plane:${RESET}"
    echo -e "  - User Workloads:     ${BLUE}http://localhost:9080${RESET} (Envoy Gateway)"
    
    if [[ "$INSTALL_BUILD_PLANE" == "true" ]]; then
        echo -e "\n${BOLD}Build Plane:${RESET}"
        echo -e "  - Argo Workflows UI:  ${BLUE}http://localhost:10081${RESET}"
        echo -e "  - Container Registry: ${BLUE}http://localhost:10082${RESET}"
    fi
    
    if [[ "$INSTALL_OBSERVABILITY" == "true" ]]; then
        echo -e "\n${BOLD}Observability Plane:${RESET}"
        echo -e "  - Observer API:            ${BLUE}http://localhost:11080${RESET}"
        echo -e "  - OpenSearch Dashboard:    ${BLUE}http://localhost:11081${RESET}"
        echo -e "  - OpenSearch API:          ${BLUE}http://localhost:11082${RESET}"
    fi

    # Check if Keel is installed
    if helm --kube-context="${KUBE_CONTEXT}" list -n keel 2>/dev/null | grep -q "^keel"; then
        echo -e "\n${BOLD}Keel (Deployment Automation):${RESET}"
        echo -e "  - UI Dashboard:           ${BLUE}http://localhost:9300${RESET} (admin/admin)"
        echo -e "  - Port-forward command:   ${YELLOW}kubectl port-forward -n keel svc/keel 9300:9300${RESET}"
    fi

    echo -e "\n${CYAN}${BOLD}Kubernetes Context:${RESET} ${KUBE_CONTEXT}"
    
    echo -e "\n${CYAN}${BOLD}Useful Commands:${RESET}"
    echo -e "  - View all resources:     ${YELLOW}kubectl --context=${KUBE_CONTEXT} get all -A${RESET}"
    echo -e "  - View DataPlane:         ${YELLOW}kubectl --context=${KUBE_CONTEXT} get dataplane -n default${RESET}"
    if [[ "$INSTALL_BUILD_PLANE" == "true" ]]; then
        echo -e "  - View BuildPlane:        ${YELLOW}kubectl --context=${KUBE_CONTEXT} get buildplane -n default${RESET}"
    fi
    if helm --kube-context="${KUBE_CONTEXT}" list -n keel 2>/dev/null | grep -q "^keel"; then
        echo -e "  - View Keel logs:         ${YELLOW}kubectl --context=${KUBE_CONTEXT} logs -n keel -l app=keel -f${RESET}"
    fi
    echo -e "  - Delete cluster:         ${YELLOW}k3d cluster delete ${CLUSTER_NAME}${RESET}"
    
    echo ""
}

# Show usage
show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Install OpenChoreo in single-cluster mode on Ubuntu.

Options:
  --skip-prerequisites           Skip prerequisite checks and installation
  --no-build-plane              Skip Build Plane installation
  --enable-observability        Install Observability Plane
  --preload-images              Preload images before installation (faster)
  --help, -h                    Show this help message

Environment Variables:
  INSTALL_BUILD_PLANE=false     Skip Build Plane installation
  INSTALL_OBSERVABILITY=true    Install Observability Plane
  SKIP_PREREQUISITES=true       Skip prerequisite checks
  PRELOAD_IMAGES=true          Preload images before installation
  USE_UBUNTU_DOCKER=true       Use Ubuntu's docker.io package (default)

Examples:
  # Full installation with all planes
  $0
  
  # Install without Build Plane
  $0 --no-build-plane
  
  # Install with Observability Plane
  $0 --enable-observability
  
  # Install with image preloading (faster for slow networks)
  $0 --preload-images
  
  # Skip prerequisite checks (if already installed)
  $0 --skip-prerequisites

EOF
}

# Parse command line arguments
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --skip-prerequisites)
                SKIP_PREREQUISITES=true
                shift
                ;;
            --no-build-plane)
                INSTALL_BUILD_PLANE=false
                shift
                ;;
            --enable-observability)
                INSTALL_OBSERVABILITY=true
                shift
                ;;
            --preload-images)
                PRELOAD_IMAGES=true
                shift
                ;;
            --help|-h)
                show_usage
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                echo "Use --help for usage information"
                exit 1
                ;;
        esac
    done
}

# Main installation flow
main() {
    echo -e "${CYAN}${BOLD}"
    cat << 'EOF'
   ___                   ____ _                            
  / _ \ _ __   ___ _ __ / ___| |__   ___  _ __ ___  ___  
 | | | | '_ \ / _ \ '_ \| |   | '_ \ / _ \| '__/ _ \/ _ \ 
 | |_| | |_) |  __/ | | | |___| | | | (_) | | |  __/ (_) |
  \___/| .__/ \___|_| |_|\____|_| |_|\___/|_|  \___|\___/ 
       |_|                                                  
EOF
    echo -e "${RESET}"
    echo -e "${BOLD}Single-Cluster Installation Script${RESET}\n"
    
    parse_args "$@"
    
    # Display configuration
    log_info "Installation Configuration:"
    log_info "  Cluster Name: ${CLUSTER_NAME}"
    log_info "  Install Build Plane: ${INSTALL_BUILD_PLANE}"
    log_info "  Install Observability: ${INSTALL_OBSERVABILITY}"
    log_info "  Skip Prerequisites: ${SKIP_PREREQUISITES}"
    log_info "  Preload Images: ${PRELOAD_IMAGES}"
    log_info "  Use Ubuntu Docker: ${USE_UBUNTU_DOCKER}"
    echo ""
    
    # Check prerequisites
    if [[ "$SKIP_PREREQUISITES" != "true" ]]; then
        check_install_jq
        check_install_docker
        check_install_k3d
        check_install_kubectl
        check_install_helm
    else
        log_info "Skipping prerequisite checks"
    fi
    
    # Create cluster
    create_cluster
    
    # Preload images if requested
    if [[ "$PRELOAD_IMAGES" == "true" ]]; then
        preload_images
    fi
    
    # Install components
    install_control_plane
    install_data_plane
    
    if [[ "$INSTALL_BUILD_PLANE" == "true" ]]; then
        install_build_plane
    fi
    
    if [[ "$INSTALL_OBSERVABILITY" == "true" ]]; then
        install_observability_plane
    fi

    # Install Keel for automatic deployment updates
    install_keel

    # Create resources
    create_dataplane_resource
    
    if [[ "$INSTALL_BUILD_PLANE" == "true" ]]; then
        create_buildplane_resource
    fi
    
    # Verify installation
    verify_installation
    
    # Print access information
    print_access_info
}

# Run main function
main "$@"
