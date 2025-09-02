/*
 * Copyright (c) 2019-2023, NVIDIA CORPORATION. All rights reserved.
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
	"log"
	"net"

	uuid "github.com/google/uuid"
	cdiresolver "github.com/kata-containers/kata-containers/src/runtime/protocols/cdiresolver"
	"google.golang.org/grpc"
)

type SandboxAPIServer struct {
	*cdiresolver.UnimplementedCDIResolverServer

	name       string
	server     *grpc.Server
	socketPath string
	stop       chan struct{} // this channel signals to stop the SAS server

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
		socketPath:           "/var/run/cdiresolver/sandboxserver.sock",
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
	// replace deviceMap
	newDeviceMap := make(map[string][]string)
	for deviceType, iommuGroups := range deviceMap {
		devs := []string{}
		physicalDevs := []string{}
		for _, dev := range iommuGroups {
			id := uuid.New().String()

			physicalDevs = append(physicalDevs, dev)
			devs = append(devs, id)
		}
		sas.deviceIDs[deviceType] = physicalDevs
		newDeviceMap[deviceType] = devs
	}
	deviceMap = newDeviceMap

	// replace iommuMap??
}

// AllocatePodDevices takes a deviceReqest (how many GPUs for a pod id)
// and returns the list of physical devices that can be given to the pod
func (sas *SandboxAPIServer) AllocatePodDevices(ctx context.Context, pr *cdiresolver.PodRequest) (*cdiresolver.PhysicalDeviceResponse, error) {
	deviceList := sas.deviceIDs[pr.DeviceType]
	if len(deviceList) < int(pr.Count) {
		return nil, fmt.Errorf("Not enough '%q' devices available for request '%v'", pr.DeviceType, pr)
	}
	response := &cdiresolver.PhysicalDeviceResponse{}
	physicalDevices := []string{}
	// podRequest has three fields : PodID, Count, DeviceType
	// get 'Count' number of GPUs from deviceIDs map
	for _ = range pr.Count {
		deviceList := sas.deviceIDs[pr.DeviceType]
		// pop the last device
		dev := deviceList[len(deviceList)-1]
		deviceList = deviceList[:len(deviceList)-1]
		sas.deviceIDs[pr.DeviceType] = deviceList

		// store the association in pod mapping
		podDevices := sas.podDeviceIDMap[pr.PodID]
		podDevices = append(podDevices, dev)
		sas.podDeviceIDMap[pr.PodID] = podDevices

		// put the physical device ID list in response structure
		physicalDevices = append(physicalDevices, dev)
	}
	response.PhysicalDeviceID = physicalDevices
	return response, nil
}

// AllocateContainerDevices takes an allocate request (pod id with virtual ids for GPUs
// meant for specific containers) and returns the physical devices that will
// be used by the container. Also updates the internal virtual ID maps that
// keep track of which physical device ids have been taken by which containers
// of a particular pod.
func (sas *SandboxAPIServer) AllocateContainerDevices(ctx context.Context, cr *cdiresolver.ContainerRequest) (*cdiresolver.PhysicalDeviceResponse, error) {
	// ContainerRequest has three fields PodID ContainerID VirtualDeviceID

	// save the container against the said pod
	containers := sas.podContainerIDMap[cr.PodID]
	containers = append(containers, cr.ContainerID)
	sas.podContainerIDMap[cr.PodID] = containers

	response := &cdiresolver.PhysicalDeviceResponse{}

	// now we get the physical devices associated with PodID
	physicalDevices := sas.podDeviceIDMap[cr.PodID]
	for _, cid := range cr.VirtualDeviceID {
		if _, ok := sas.virtualDeviceIDMap[cid]; ok {
			err := fmt.Errorf("Virtual ID %s is already taken", cid)
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
			// assign it to cid
			containerDevices := sas.containerDeviceIDMap[cr.ContainerID]
			containerDevices = append(containerDevices, cid)
			sas.containerDeviceIDMap[cr.ContainerID] = containerDevices
			sas.virtualDeviceIDMap[cid] = physDev
			sas.deviceVirtualIDMap[physDev] = cid
			response.PhysicalDeviceID = append(response.PhysicalDeviceID, physDev)
			assigned = true
			break
		}
		if !assigned {
			return response, fmt.Errorf("Could not find a suitable physical device for %q. Request: %v", cid, cr)
		}
	}
	return response, nil
}

func (sas *SandboxAPIServer) FreeContainerDevices(ctx context.Context, cr *cdiresolver.ContainerRequest) (*cdiresolver.PhysicalDeviceResponse, error) {
	response := &cdiresolver.PhysicalDeviceResponse{}
	// dis-associate container's virtual ids with the physical devices
	removeMap := make(map[string]bool)
	for _, cid := range cr.VirtualDeviceID {
		removeMap[cid] = true
	}
	vids := sas.containerDeviceIDMap[cr.ContainerID]
	remainingVirtualDevices := []string{}
	for _, vid := range vids {
		if !removeMap[vid] {
			remainingVirtualDevices = append(remainingVirtualDevices, vid)
		} else {
			// remove the virtual to physical mapping
			physDev := sas.virtualDeviceIDMap[vid]
			delete(sas.virtualDeviceIDMap, vid)
			delete(sas.deviceVirtualIDMap, physDev)
			response.PhysicalDeviceID = append(response.PhysicalDeviceID, physDev)
		}
	}
	sas.containerDeviceIDMap[cr.ContainerID] = remainingVirtualDevices
	return response, nil
}

func (sas *SandboxAPIServer) FreePodDevices(ctx context.Context, pr *cdiresolver.PodRequest) (*cdiresolver.PhysicalDeviceResponse, error) {
	// remove all physical devices associated with pod
	response := &cdiresolver.PhysicalDeviceResponse{}
	response.PhysicalDeviceID = sas.podDeviceIDMap[pr.PodID]
	sas.podDeviceIDMap[pr.PodID] = nil

	// put the devices back into available device list for the give type
	sas.deviceIDs[pr.DeviceType] = append(sas.deviceIDs[pr.DeviceType], response.PhysicalDeviceID...)
	return response, nil
}

func (sas *SandboxAPIServer) cleanup() error {
	return nil
}

// Start starts the gRPC server of the sandboxAPIServer
func (sas *SandboxAPIServer) Start(stop chan struct{}) error {
	if sas.server != nil {
		return fmt.Errorf("gRPC server already started")
	}

	sas.stop = stop

	err := sas.cleanup()
	if err != nil {
		return err
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
