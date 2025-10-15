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
	"reflect"
	"strings"
	"testing"

	cdiresolver "github.com/kata-containers/kata-containers/src/runtime/protocols/cdiresolver"
)

func setupServer() *SandboxAPIServer {
	sas := NewSandboxAPIServer()
	//sas.Start(make(chan struct{}))
	return sas
}

// UNIT TESTS
func TestAllocatePodDevices(t *testing.T) {
	sas := setupServer()
	deviceType := "gpu-1"
	sas.deviceIDs[deviceType] = []string{"phys-dev-1", "phys-dev-2", "phys-dev-3"}

	tests := []struct {
		name                  string
		podRequest            *cdiresolver.PodRequest
		expectedPhysDevs      []string
		expectedRemainingDevs []string
		expectedPodDevs       []string
		expectError           bool
	}{
		{
			name: "Allocate two devices to a pod",
			podRequest: &cdiresolver.PodRequest{
				PodID:      "pod-1",
				Count:      2,
				DeviceType: deviceType,
			},
			expectedPhysDevs:      []string{"phys-dev-3", "phys-dev-2"},
			expectedRemainingDevs: []string{"phys-dev-1"},
			expectedPodDevs:       []string{"phys-dev-3", "phys-dev-2"},
			expectError:           false,
		},
		{
			name: "Allocate one more device to the same pod",
			podRequest: &cdiresolver.PodRequest{
				PodID:      "pod-1",
				Count:      1,
				DeviceType: deviceType,
			},
			expectedPhysDevs:      []string{"phys-dev-1"},
			expectedRemainingDevs: []string{},
			expectedPodDevs:       []string{"phys-dev-3", "phys-dev-2", "phys-dev-1"},
			expectError:           false,
		},
		{
			name: "Allocate more devices than available",
			podRequest: &cdiresolver.PodRequest{
				PodID:      "pod-2",
				Count:      1,
				DeviceType: "non-existent-type",
			},
			expectedPhysDevs:      nil,
			expectedRemainingDevs: nil,
			expectedPodDevs:       nil,
			expectError:           true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := sas.AllocatePodDevices(context.Background(), tc.podRequest)
			if (err != nil) != tc.expectError {
				t.Fatalf("AllocatePodDevices returned unexpected error state: %v", err)
			}
			if tc.expectError {
				return
			}
			if !reflect.DeepEqual(resp.PhysicalDeviceID, tc.expectedPhysDevs) {
				t.Errorf("Expected physical devices %v, got %v", tc.expectedPhysDevs, resp.PhysicalDeviceID)
			}
			if !reflect.DeepEqual(sas.deviceIDs[tc.podRequest.DeviceType], tc.expectedRemainingDevs) {
				t.Errorf("Expected remaining devices %v, got %v", tc.expectedRemainingDevs, sas.deviceIDs[tc.podRequest.DeviceType])
			}
			if !reflect.DeepEqual(sas.podDeviceIDMap[tc.podRequest.PodID], tc.expectedPodDevs) {
				t.Errorf("Expected pod devices %v, got %v", tc.expectedPodDevs, sas.podDeviceIDMap[tc.podRequest.PodID])
			}
		})
	}
}

func TestAllocateContainerDevices(t *testing.T) {
	sas := setupServer()
	podID := "pod-1"
	containerID1 := "container-1"
	containerID2 := "container-2"

	sas.podDeviceIDMap[podID] = []string{"phys-dev-1", "phys-dev-2"}

	tests := []struct {
		name              string
		containerRequest  *cdiresolver.ContainerRequest
		expectedPhysDevs  []string
		expectedErr       bool
		expectedMapsAfter func(resp *cdiresolver.PhysicalDeviceResponse)
	}{
		{
			name: "Allocate virtual ID to container",
			containerRequest: &cdiresolver.ContainerRequest{
				PodID:           podID,
				ContainerID:     containerID1,
				VirtualDeviceID: []string{"virt-dev-1"},
			},
			expectedPhysDevs: []string{"phys-dev-1"},
			expectedErr:      false,
			expectedMapsAfter: func(resp *cdiresolver.PhysicalDeviceResponse) {
				if len(resp.PhysicalDeviceID) != 1 {
					t.Errorf("Bad count of physical devices in response '%v'", resp.PhysicalDeviceID)
				}
				if resp.PhysicalDeviceID[0] != "phys-dev-1" && resp.PhysicalDeviceID[0] != "phys-dev-2" {
					t.Errorf("Bad response")
				}
				if sas.virtualDeviceIDMap["virt-dev-1"] != "phys-dev-1" && sas.virtualDeviceIDMap["virt-dev-1"] != "phys-dev-2" {
					t.Errorf("virtualDeviceIDMap not updated correctly")
				}
				if sas.deviceVirtualIDMap["phys-dev-1"] != "virt-dev-1" && sas.deviceVirtualIDMap["phys-dev-2"] != "virt-dev-1" {
					t.Errorf("deviceVirtualIDMap not updated correctly")
				}
				if !reflect.DeepEqual(sas.containerDeviceIDMap[containerID1], []string{"virt-dev-1"}) {
					t.Errorf("containerDeviceIDMap not updated correctly")
				}
			},
		},
		{
			name: "Allocate another virtual ID to a different container",
			containerRequest: &cdiresolver.ContainerRequest{
				PodID:           podID,
				ContainerID:     containerID2,
				VirtualDeviceID: []string{"virt-dev-2"},
			},
			expectedPhysDevs: []string{"phys-dev-2"},
			expectedErr:      false,
			expectedMapsAfter: func(resp *cdiresolver.PhysicalDeviceResponse) {
				if len(resp.PhysicalDeviceID) != 1 {
					t.Errorf("Bad count of physical devices in response")
				}
				if resp.PhysicalDeviceID[0] != "phys-dev-1" && resp.PhysicalDeviceID[0] != "phys-dev-2" {
					t.Errorf("Bad response")
				}
				if sas.virtualDeviceIDMap["virt-dev-2"] != "phys-dev-2" && sas.virtualDeviceIDMap["virt-dev-2"] != "phys-dev-1" {
					t.Errorf("virtualDeviceIDMap not updated correctly")
				}
				if sas.deviceVirtualIDMap["phys-dev-1"] != "virt-dev-2" && sas.deviceVirtualIDMap["phys-dev-2"] != "virt-dev-2" {
					t.Errorf("deviceVirtualIDMap not updated correctly")
				}
				if !reflect.DeepEqual(sas.containerDeviceIDMap[containerID2], []string{"virt-dev-2"}) {
					t.Errorf("containerDeviceIDMap not updated correctly")
				}
			},
		},
		{
			name: "Allocate an already taken virtual ID",
			containerRequest: &cdiresolver.ContainerRequest{
				PodID:           podID,
				ContainerID:     containerID1,
				VirtualDeviceID: []string{"virt-dev-1"},
			},
			expectedPhysDevs:  nil,
			expectedErr:       true,
			expectedMapsAfter: func(resp *cdiresolver.PhysicalDeviceResponse) { /* No change expected */ },
		},
		{
			name: "Allocate multiple virtual IDs to a single container when no more physical devices remain",
			containerRequest: &cdiresolver.ContainerRequest{
				PodID:           podID,
				ContainerID:     containerID1,
				VirtualDeviceID: []string{"virt-dev-3", "virt-dev-4"},
			},
			expectedPhysDevs:  []string{"phys-dev-1", "phys-dev-2"},
			expectedErr:       true,
			expectedMapsAfter: func(resp *cdiresolver.PhysicalDeviceResponse) { /* No change expected */ },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset maps for each test case that mutates state
			if strings.Contains(tc.name, "Allocate virtual ID") || strings.Contains(tc.name, "Allocate another virtual ID") {
				sas := setupServer()
				sas.podDeviceIDMap[podID] = []string{"phys-dev-1", "phys-dev-2"}
			}

			resp, err := sas.AllocateContainerDevices(context.Background(), tc.containerRequest)
			if (err != nil) != tc.expectedErr {
				t.Fatalf("Expected error %v, got %v", tc.expectedErr, err)
			}
			if err == nil {
				if !reflect.DeepEqual(resp.PhysicalDeviceID, tc.expectedPhysDevs) {
					t.Errorf("Expected physical devices %v, got %v", tc.expectedPhysDevs, resp.PhysicalDeviceID)
				}
				tc.expectedMapsAfter(resp)
			}
		})
	}
}

func TestFreeContainerDevices(t *testing.T) {
	sas := setupServer()
	podID := "pod-1"
	containerID := "container-1"

	sas.podContainerIDMap[podID] = []string{containerID}
	sas.containerDeviceIDMap[containerID] = []string{"virt-dev-1", "virt-dev-2"}
	sas.virtualDeviceIDMap["virt-dev-1"] = "phys-dev-1"
	sas.virtualDeviceIDMap["virt-dev-2"] = "phys-dev-2"
	sas.deviceVirtualIDMap["phys-dev-1"] = "virt-dev-1"
	sas.deviceVirtualIDMap["phys-dev-2"] = "virt-dev-2"

	tests := []struct {
		name                  string
		containerRequest      *cdiresolver.ContainerRequest
		expectedFreedDevs     []string
		expectedRemainingVids []string
	}{
		{
			name: "Free one device from a container",
			containerRequest: &cdiresolver.ContainerRequest{
				ContainerID:     containerID,
				VirtualDeviceID: []string{"virt-dev-1"},
			},
			expectedFreedDevs:     []string{"phys-dev-1"},
			expectedRemainingVids: []string{"virt-dev-2"},
		},
		{
			name: "Free the last remaining device",
			containerRequest: &cdiresolver.ContainerRequest{
				ContainerID:     containerID,
				VirtualDeviceID: []string{"virt-dev-2"},
			},
			expectedFreedDevs:     []string{"phys-dev-2"},
			expectedRemainingVids: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := sas.FreeContainerDevices(context.Background(), tc.containerRequest)
			if err != nil {
				t.Fatalf("FreeContainerDevices returned an unexpected error: %v", err)
			}
			if !reflect.DeepEqual(resp.PhysicalDeviceID, tc.expectedFreedDevs) {
				t.Errorf("Expected freed physical devices %v, got %v", tc.expectedFreedDevs, resp.PhysicalDeviceID)
			}
			if !reflect.DeepEqual(sas.containerDeviceIDMap[containerID], tc.expectedRemainingVids) {
				t.Errorf("Expected remaining virtual devices %v, got %v", tc.expectedRemainingVids, sas.containerDeviceIDMap[containerID])
			}
			for _, vid := range tc.containerRequest.VirtualDeviceID {
				if _, ok := sas.virtualDeviceIDMap[vid]; ok {
					t.Errorf("virtualDeviceIDMap mapping for %s was not deleted", vid)
				}
				physDev := sas.virtualDeviceIDMap[vid]
				if _, ok := sas.deviceVirtualIDMap[physDev]; ok {
					t.Errorf("deviceVirtualIDMap mapping for %s was not deleted", physDev)
				}
			}
		})
	}
}

func TestFreePodDevices(t *testing.T) {
	sas := setupServer()
	podID := "pod-1"
	deviceType := "gpu-1"

	sas.podDeviceIDMap[podID] = []string{"phys-dev-1", "phys-dev-2"}
	sas.deviceIDs[deviceType] = []string{"phys-dev-3"}

	req := &cdiresolver.PodRequest{
		PodID:      podID,
		DeviceType: deviceType,
	}

	resp, err := sas.FreePodDevices(context.Background(), req)
	if err != nil {
		t.Fatalf("FreePodDevices returned an unexpected error: %v", err)
	}

	expectedFreedDevs := []string{"phys-dev-1", "phys-dev-2"}
	if !reflect.DeepEqual(resp.PhysicalDeviceID, expectedFreedDevs) {
		t.Errorf("Expected freed physical devices %v, got %v", expectedFreedDevs, resp.PhysicalDeviceID)
	}

	if len(sas.podDeviceIDMap[podID]) != 0 {
		t.Errorf("Pod device map was not cleared. Remaining: %v", sas.podDeviceIDMap[podID])
	}

	expectedAvailableDevs := []string{"phys-dev-3", "phys-dev-1", "phys-dev-2"}
	if !reflect.DeepEqual(sas.deviceIDs[deviceType], expectedAvailableDevs) {
		t.Errorf("Expected available devices %v, got %v", expectedAvailableDevs, sas.deviceIDs[deviceType])
	}
}
