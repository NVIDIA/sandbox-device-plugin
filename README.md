# NVIDIA K8s Device Plugin to assign Passthrough GPUs to Kata VMs for Confidential Containers

## Table of Contents
- [About](#about)
- [Features](#features)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Docs](#docs)

## About
This is a kubernetes device plugin that can discover and expose GPUs for passthrough on a kubernetes node. This device plugin will enable to launch GPU attached [Kata](https://katacontainers.io/) VM based containers in your kubernetes cluster. Its specifically developed to serve Kata workloads in a Kubernetes cluster.


## Features
- Discovers Nvidia GPUs which are bound to VFIO-PCI driver and exposes them as devices available to be attached to VM in pass through mode.
- Performs basic health check on the GPU on a kubernetes node.

## Prerequisites
- Need to have Nvidia GPU configured for GPU passthrough. Quickstart section provides details about this
- Kubernetes version >= v1.11
- Kata release >= v3.23.0

## Quick Start

Before starting the device plug, the GPUs on a kubernetes node need to configured to be in GPU pass through mode.

### Preparing a GPU to be used in pass through mode
GPU needs to be loaded with VFIO-PCI driver to be used in pass through mode

##### 1. Enable IOMMU and blacklist nouveau driver on KVM Host

  Append "**intel_iommu=on modprobe.blacklist=nouveau**" to "**GRUB_CMDLINE_LINUX**" 
```shell
$ vi /etc/default/grub
# line 6: add (if AMD CPU, add [amd_iommu=on])
GRUB_TIMEOUT=5
GRUB_DISTRIBUTOR="$(sed 's, release .*$,,g' /etc/system-release)"
GRUB_DEFAULT=saved
GRUB_DISABLE_SUBMENU=true
GRUB_TERMINAL_OUTPUT="console"
GRUB_CMDLINE_LINUX="rd.lvm.lv=centos/root rd.lvm.lv=centos/swap rhgb quiet intel_iommu=on modprobe.blacklist=nouveau"
GRUB_DISABLE_RECOVERY="true"
```
###### Legacy Mode (BIOS)
```shell 
grub2-mkconfig -o /boot/grub2/grub.cfg
reboot
```
###### UEFI Mode
```shell 
grub2-mkconfig -o /boot/efi/EFI/centos/grub.cfg
reboot
```

After rebooting, verify IOMMU is enabled using following command
```shell
dmesg | grep -E "DMAR|IOMMU"
```
Verify that nouveau is disabled
```shell
dmesg | grep -i nouveau
```

##### 2. Enable vfio-pci kernel module

**Determine vendor-ID and device-ID of the GPU using following command**

```shell
lspci -nn | grep -i nvidia
```
In the example below the vendor-ID is 10de and device-ID is 1b38
```shell
$ lspci -nn | grep -i nvidia
04:00.0 3D controller [0302]: NVIDIA Corporation GP102GL [Tesla P40] [10de:1b38] (rev a1)
```

**Update VFIO config**
```shell
echo "options vfio-pci ids=vendor-ID:device-ID" > /etc/modprobe.d/vfio.conf
```
Considering vendor-ID is 10de and device-ID is 1b38 command will be as follows
```shell
echo "options vfio-pci ids=10de:1b38" > /etc/modprobe.d/vfio.conf
```
**Update config to load VFIO-PCI module after reboot**
```shell
echo 'vfio-pci' > /etc/modules-load.d/vfio-pci.conf
reboot
```

**Verify VFIO-PCI driver is loaded for the GPU**
```shell
lspci -nnk -d 10de:
```
Output below shows that "Kernel driver in use" is "vfio-pci"
```shell
$ lspci -nnk -d 10de:
04:00.0 3D controller [0302]: NVIDIA Corporation GP102GL [Tesla P40] [10de:1b38] (rev a1)
        Subsystem: NVIDIA Corporation Device [10de:11d9]
        Kernel driver in use: vfio-pci
        Kernel modules: nouveau
```

## Docs
### Deployment
The daemon set creation yaml can be used to deploy the device plugin. 
```
kubectl apply -f nvidia-sandbox-device-plugin.yaml
```

Example YAMLs for creating VMs with GPU/vGPU are in the `examples` folder

### Build

Change to proper DOCKER_REPO and DOCKER_TAG env before building images
e.g.
```shell
export DOCKER_REPO="quay.io/nvidia/nvidia-sandbox-device-plugin"
export DOCKER_TAG=devel
```

Build executable binary using make
```shell
make
```
Build docker image
```shell
make build-image DOCKER_REPO=<docker-repo-url> DOCKER_TAG=<image-tag>
```
Push docker image to a docker repo
```shell
make push-image DOCKER_REPO=<docker-repo-url> DOCKER_TAG=<image-tag>
```
### Fabric Manager partition-aware allocation (Shared NVSwitch)

On NVSwitch systems running Fabric Manager in the Shared NVSwitch model, NVLink between a VM's
GPUs only works when those GPUs form an activated FM fabric partition. `GetPreferredAllocation`
uses the FM SDK (`fmGetSupportedFabricPartitions`) to prefer device sets that match a supported
partition of the requested size, so multi-GPU pods get working NVLink.

It is opt-in and off by default. To enable:

- Build with libnvfm support: `CGO_ENABLED=1 go build -tags=nvfm ./...` (needs the
  `nvidia-fabric-manager-devel` package; the default build ships a stub and needs no SDK). A
  binary built without the tag refuses to enable FM even if the env var is set.
- Set `ENABLE_FABRIC_MANAGER=true`. Optional: `FM_ADDRESS` (default `127.0.0.1:6666`),
  `FM_ADDRESS_TYPE` (`inet`|`unix`), `GPU_PCI_MODULE_MAPPING_PATH` (default
  `/run/nvidia-fabricmanager/gpu-pci-module-mapping.json`).
- The deployment must let the plugin reach FM's command API and read the mapping file — i.e.
  the FM endpoint has to be reachable from the pod (host loopback needs `hostNetwork`) and the
  mapping path host-mounted. The mapping is a JSON object of PCI BDF to GPU module id; it is
  required because BDF order is not module-id order.

If FM is unreachable or nothing matches, the plugin returns no preference and the devicemanager
allocates as usual, so this can never fail an allocation. Automatic partition activation and
generation of the mapping file are out of scope.

### To Do
- Improve the healthcheck mechanism for GPUs with VFIO-PCI drivers
- Build the container image with `-tags=nvfm` on all target arches (the FM SDK install is
  currently x86_64 only) and add a libnvfm cgo build/test path to CI
--------------------------------------------------------------
