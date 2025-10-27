/*
 * Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 *  * Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 *  * Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *  * Neither the name of NVIDIA CORPORATION nor the names of its
 *    contributors may be used to endorse or promote products derived
 *    from this software without specific prior written permission.
 *
 * THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS ``AS IS'' AND ANY
 * EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
 * IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR
 * PURPOSE ARE DISCLAIMED.  IN NO EVENT SHALL THE COPYRIGHT OWNER OR
 * CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
 * EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
 * PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
 * PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY
 * OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
 * (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
 * OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
 */

package device_plugin

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"log"
	"net"
	"os"
	"path/filepath"

	uuid "github.com/google/uuid"
	cdiresolver "github.com/kata-containers/kata-containers/src/runtime/protocols/cdiresolver"
	"google.golang.org/grpc"
)

type SandboxAPIServer struct {
	*cdiresolver.UnimplementedCDIResolverServer

	name       string
	server     *grpc.Server
	socketPath string

	// list of physical device ids associated with each GPU type
	deviceIDs map[string][]string

	// map of virtual ids to physical ids, built lazily
	virtualDeviceIDMap map[string]string

	// map of physical device ids to virtual ids, built lazily
	deviceVirtualIDMap map[string]string

	// map of pod ids to physical device ids associated with it
	podDeviceIDMap map[string][]string

	// map of pod ids and containers that belong to it
	podContainerIDMap map[string][]string

	// map of container ids and virtual device ids allocated to it
	containerDeviceIDMap map[string][]string
}

func NewSandboxAPIServer() *SandboxAPIServer {
	return &SandboxAPIServer{
		name:                 "CDIResolver",
		socketPath:           cdiresolverPath + "/sandboxserver.sock",
		deviceIDs:            make(map[string][]string),
		virtualDeviceIDMap:   make(map[string]string),
		deviceVirtualIDMap:   make(map[string]string),
		podDeviceIDMap:       make(map[string][]string),
		podContainerIDMap:    make(map[string][]string),
		containerDeviceIDMap: make(map[string][]string),
	}
}

func (sas *SandboxAPIServer) getVirtualID(physicalDeviceID string) string {
	// get the virtual id mapping if it exists, or create one
	value, ok := sas.deviceVirtualIDMap[physicalDeviceID]
	if !ok {
		log.Printf("Device %s not found in virtual mapping", physicalDeviceID)
	}
	return value
}

// mapDevicesToVirtualSpace changes the data stored in deviceMap, iommuMap etc
// in such a way that all physical devices get a virtual id
// the actual physical ids are stored in deviceIDs map
// this function should be called after iommuMap, deviceMap have been populated
func (sas *SandboxAPIServer) MapDevicesToVirtualSpace() {
	// clean out any stray/stale files from a prior run
	err := sas.cleanup()
	if err != nil {
		log.Printf("Failed to clean out cdiresolver path: %v", err)
		//return
	}

	// replace deviceMap
	newDeviceMap := make(map[string][]string)
	for deviceType, iommuGroups := range deviceMap {
		devs := []string{}
		physicalDevs := []string{}
		for _, dev := range iommuGroups {
			id := uuid.New().String()
			// create a character device in cdiresolver path so that containerd thinks its a real device
			if err := unix.Mknod(cdiresolverPath+"/"+id, unix.S_IFCHR|uint32(0600), int(unix.Mkdev(uint32(10), uint32(1010)))); err != nil {
				fmt.Printf("Error creating character device: %v\n", err)
			}

			physicalDevs = append(physicalDevs, dev)
			devs = append(devs, id)
		}
		deviceTypeKey := deviceType
		if PGPUAlias != "" {
			deviceTypeKey = fmt.Sprintf("nvidia.com/%s", PGPUAlias)
		}
		sas.deviceIDs[deviceTypeKey] = physicalDevs
		newDeviceMap[deviceTypeKey] = devs
	}
	deviceMap = newDeviceMap
	log.Printf("[Cold Plug] Original physical devices: %v", sas.deviceIDs)
}

// resolveDevicePath takes in a physical device and returns the host path
// that points to this device. e.g. iommuId 19 will resolve to /dev/vfio/19
// or /dev/vfio/devices/vfio0 in the case when iommufd is supported
func (sas *SandboxAPIServer) resolveDevicePath(devId string) (string, error) {
	hostPath := ""
	iommufdSupported, err := supportsIOMMUFD()
	if err != nil {
		return "", fmt.Errorf("could not determine iommufd support: %w", err)
	}
	if iommufdSupported {
		// get the dev from iommuMap
		devs, exists := iommuMap[devId]
		if !exists || len(devs) != 1 {
			return "", fmt.Errorf("device does not exist or has more than one addresses: %v", devId)
		}
		// pick the first one (only one)
		dev := devs[0]
		vfiodev, err := readVFIODev(basePath, dev.addr)
		if err != nil {
			return "", fmt.Errorf("could not determine iommufd device for device %s: %v", dev.addr, err)
		}
		hostPath = filepath.Join(vfioDevicePath, "devices", vfiodev)
	} else {
		hostPath = filepath.Join(vfioDevicePath, devId)
	}
	return hostPath, nil
}

// AllocatePodDevices takes a deviceReqest (how many GPUs for a pod id)
// and returns the list of physical devices that can be given to the pod
func (sas *SandboxAPIServer) AllocatePodDevices(ctx context.Context, pr *cdiresolver.PodRequest) (*cdiresolver.PhysicalDeviceResponse, error) {
	log.Printf("[Info] Pod Allocate Request: %v", pr)

	response := &cdiresolver.PhysicalDeviceResponse{}

	// check if this pod already has some devices assigned
	podDevices, exists := sas.podDeviceIDMap[pr.PodID]
	if exists && len(podDevices) == int(pr.Count) {
		response.PhysicalDeviceID = podDevices
		log.Printf("[Info] Pod (already exists) Allocate Response: %v", response)
		return response, nil
	}

	deviceType := pr.DeviceType
	deviceList := sas.deviceIDs[deviceType]
	if len(deviceList) < int(pr.Count) {
		err := fmt.Errorf("Not enough '%q' devices available for request '%v'", deviceType, pr)
		log.Print(err)
		return nil, err
	}
	physicalDevices := []string{}
	// podRequest has three fields : PodID, Count, DeviceType
	// get 'Count' number of GPUs from deviceIDs map
	for _ = range pr.Count {
		deviceList := sas.deviceIDs[deviceType]
		// pop the last device
		dev := deviceList[len(deviceList)-1]
		deviceList = deviceList[:len(deviceList)-1]
		sas.deviceIDs[deviceType] = deviceList

		// store the association in pod mapping
		podDevices = sas.podDeviceIDMap[pr.PodID]
		podDevices = append(podDevices, dev)
		sas.podDeviceIDMap[pr.PodID] = podDevices

		// put the physical device ID list in response structure
		// in fact put the iommu_fd/iommu_id instead of the dev itself
		physicalDevices = append(physicalDevices, dev)
	}
	response.PhysicalDeviceID = physicalDevices

	log.Printf("[Info] Pod Allocate Response: %v", response)
	return response, nil
}

// AllocateContainerDevices takes an allocate request (pod id with virtual ids for GPUs
// meant for specific containers) and returns the physical devices that will
// be used by the container. Also updates the internal virtual ID maps that
// keep track of which physical device ids have been taken by which containers
// of a particular pod.
func (sas *SandboxAPIServer) AllocateContainerDevices(ctx context.Context, cr *cdiresolver.ContainerRequest) (*cdiresolver.PhysicalDeviceResponse, error) {
	// ContainerRequest has three fields PodID ContainerID VirtualDeviceID
	log.Printf("[Info] Container Allocate Request: %v", cr)

	// save the container against the said pod
	containers := sas.podContainerIDMap[cr.PodID]
	containers = append(containers, cr.ContainerID)
	sas.podContainerIDMap[cr.PodID] = containers

	response := &cdiresolver.PhysicalDeviceResponse{}

	// now we get the physical devices associated with PodID
	physicalDevices := sas.podDeviceIDMap[cr.PodID]
	for _, vid := range cr.VirtualDeviceID {
		if _, ok := sas.virtualDeviceIDMap[vid]; ok {
			err := fmt.Errorf("Virtual Device ID %s is already taken", vid)
			log.Print(err)
			return nil, err
		}

		// take a physical device ID that is not taken
		// it simply will not have an entry in the tables virtualDeviceIDMap and deviceVirtualIDMap
		assigned := false
		for _, physDev := range physicalDevices {
			_, ok := sas.deviceVirtualIDMap[physDev]
			if ok {
				continue
			}
			devicePath, err := sas.resolveDevicePath(physDev)
			if err != nil {
				// bad device
				log.Printf("Bad device %v in ContainerAllocate: %v", physDev, err)
				continue
			}
			// assign it to vid
			containerDevices := sas.containerDeviceIDMap[cr.ContainerID]
			containerDevices = append(containerDevices, vid)
			sas.containerDeviceIDMap[cr.ContainerID] = containerDevices
			sas.virtualDeviceIDMap[vid] = physDev
			sas.deviceVirtualIDMap[physDev] = vid
			response.PhysicalDeviceID = append(response.PhysicalDeviceID, devicePath)
			assigned = true
			break
		}
		if !assigned {
			err := fmt.Errorf("Could not find a suitable physical device for %q. Request: %v", vid, cr)
			log.Print(err)
			return response, err
		}
	}
	log.Printf("[Info] Container Allocate Response: %v", response)
	return response, nil
}

func (sas *SandboxAPIServer) FreeContainerDevices(ctx context.Context, cr *cdiresolver.ContainerRequest) (*cdiresolver.PhysicalDeviceResponse, error) {
	log.Printf("[Info] Free Container Request: %v", cr)
	response := &cdiresolver.PhysicalDeviceResponse{}
	// dis-associate container's virtual ids with the physical devices
	vids := sas.containerDeviceIDMap[cr.ContainerID]
	for _, vid := range vids {
		// remove the virtual to physical mapping
		physDev := sas.virtualDeviceIDMap[vid]
		delete(sas.virtualDeviceIDMap, vid)
		delete(sas.deviceVirtualIDMap, physDev)
		response.PhysicalDeviceID = append(response.PhysicalDeviceID, physDev)
	}
	delete(sas.containerDeviceIDMap, cr.ContainerID)

	// remove container from pod's container map
	containers := sas.podContainerIDMap[cr.PodID]
	for i, cid := range containers {
		if cid == cr.ContainerID {
			containers = append(containers[:i], containers[i+1:]...)
			break
		}
	}
	sas.podContainerIDMap[cr.PodID] = containers

	log.Printf("[Info] Free Container Response: %v", response)
	return response, nil
}

func (sas *SandboxAPIServer) FreePodDevices(ctx context.Context, pr *cdiresolver.PodRequest) (*cdiresolver.PhysicalDeviceResponse, error) {
	log.Printf("[INFO] Free Pod Devices request: %v", pr)
	// remove all physical devices associated with pod
	response := &cdiresolver.PhysicalDeviceResponse{}
	response.PhysicalDeviceID = sas.podDeviceIDMap[pr.PodID]
	delete(sas.podDeviceIDMap, pr.PodID)

	// put the devices back into available device list for the given type
	deviceType := pr.DeviceType
	sas.deviceIDs[deviceType] = append(sas.deviceIDs[deviceType], response.PhysicalDeviceID...)

	// also double check and free the virtual device IDs associated with these physical devices
	// get containers for the pod:
	containers := sas.podContainerIDMap[pr.PodID]
	for _, cid := range containers {
		vids := sas.containerDeviceIDMap[cid]
		// remove the physical id mappings of these virtual ids
		for _, vid := range vids {
			physDev := sas.virtualDeviceIDMap[vid]
			delete(sas.virtualDeviceIDMap, vid)
			delete(sas.deviceVirtualIDMap, physDev)
		}
	}
	delete(sas.podContainerIDMap, pr.PodID)
	// in case there is some physical device assigned to the pod
	// and mapped to a virtual device, but never had a container associated with it
	// we still need to clean it up
	for _, pdev := range response.PhysicalDeviceID {
		delete(sas.deviceVirtualIDMap, pdev)
	}

	log.Printf("[INFO] Free Pod Devices response: %v", response)
	return response, nil
}

func (sas *SandboxAPIServer) cleanup() error {
	return os.RemoveAll(cdiresolverPath)
}

// Stop stops the gRPC server
func (sas *SandboxAPIServer) Stop() error {
	sas.server.Stop()
	sas.server = nil

	return sas.cleanup()
}

// Start starts the gRPC server of the sandboxAPIServer
func (sas *SandboxAPIServer) Start() error {
	if sas.server != nil {
		return fmt.Errorf("gRPC server already started")
	}

	sock, err := net.Listen("unix", sas.socketPath)
	if err != nil {
		log.Printf("[%s] Error creating GRPC server socket for CDI resolver: %v", sas.name, err)
		return err
	}

	sas.server = grpc.NewServer([]grpc.ServerOption{}...)
	cdiresolver.RegisterCDIResolverServer(sas.server, sas)

	go sas.server.Serve(sock)

	err = waitForGrpcServer(sas.socketPath, connectionTimeout)
	if err != nil {
		// this err is returned at the end of the Start function
		log.Printf("[%s] Error connecting to GRPC server: %v", sas.name, err)
	}

	log.Println(sas.name + " API server ready")

	return err
}
