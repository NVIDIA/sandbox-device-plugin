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
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/NVIDIA/go-nvlib/pkg/nvpci"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// NvidiaPCIDevice holds details about an NVIDIA PCI device (GPU or NVSwitch)
type NvidiaPCIDevice struct {
	Address    string // PCI address of device
	DeviceID   uint16 // PCI device ID
	DeviceName string // Human-readable device name
	IommuGroup int    // IOMMU group number
	IommuFD    string // IOMMUFD device handle (if available)
	IsNVSwitch bool   // True if this is an NVSwitch device
}

// iommuMap maps IOMMU group/fd key to list of devices in that group
var iommuMap map[string][]NvidiaPCIDevice

// deviceMap maps device ID to list of IOMMU group/fd keys for that device type
var deviceMap map[string][]string

// nvSwitchDeviceIDs tracks which device IDs are NVSwitches
var nvSwitchDeviceIDs map[string]bool

// nvpciLib is the nvpci interface for device discovery (injectable for testing)
var nvpciLib nvpci.Interface

var startDevicePlugin = startDevicePluginFunc
var stop = make(chan struct{})
var PGPUAlias string
var NVSwitchAlias string

func InitiateDevicePlugin() {
	// Initialize nvpci library if not already set (allows injection for testing)
	if nvpciLib == nil {
		nvpciLib = nvpci.New()
	}
	// Discover NVIDIA devices bound to vfio-pci driver
	createIommuDeviceMap()
	GenerateCDISpec()
	createDevicePlugins()
}

// createDevicePlugins starts a device plugin for each distinct NVIDIA device type
func createDevicePlugins() {
	var devicePlugins []*GenericDevicePlugin
	var devs []*pluginapi.Device
	iommufdSupported, err := supportsIOMMUFD()
	if err != nil {
		log.Printf("Could not find if IOMMU FD is supported: %v", err)
		return
	}
	log.Printf("Iommu Map %v", iommuMap)
	log.Printf("Device Map %v", deviceMap)
	log.Println("Iommu FD support: ", iommufdSupported)

	// Iterate over deviceMap to create device plugin for each type of device on the host
	for deviceID, iommuKeys := range deviceMap {
		devs = nil
		for _, iommuKey := range iommuKeys {
			devs = append(devs, &pluginapi.Device{
				ID:     iommuKey,
				Health: pluginapi.Healthy,
			})
		}

		// Determine device name - use alias if set, otherwise use actual device name
		var deviceName string
		if isNVSwitchDeviceID(deviceID) {
			if NVSwitchAlias != "" {
				deviceName = NVSwitchAlias
			} else {
				deviceName = getDeviceNameForID(deviceID)
			}
		} else if PGPUAlias != "" {
			deviceName = PGPUAlias
		} else {
			deviceName = getDeviceNameForID(deviceID)
		}

		if deviceName == "" {
			log.Printf("Error: Could not find device name for device id: %s", deviceID)
			deviceName = deviceID
		}

		log.Printf("DP Name %s, devs: %v", deviceName, devs)
		devicePath := "/dev/vfio/"
		if iommufdSupported {
			devicePath = "/dev/vfio/devices/"
		}
		dp := NewGenericDevicePlugin(deviceName, devicePath, devs)
		err := startDevicePlugin(dp)
		if err != nil {
			log.Printf("Error starting %s device plugin: %v", dp.deviceName, err)
		} else {
			devicePlugins = append(devicePlugins, dp)
		}
	}
	<-stop

	log.Printf("Shutting down device plugin controller")
	for _, v := range devicePlugins {
		v.Stop()
	}
}

func startDevicePluginFunc(dp *GenericDevicePlugin) error {
	return dp.Start(stop)
}

// createIommuDeviceMap discovers all NVIDIA GPUs and NVSwitches bound to vfio-pci driver
func createIommuDeviceMap() {
	iommufdSupported, err := supportsIOMMUFD()
	if err != nil {
		log.Printf("Could not find if IOMMU FD is supported: %v", err)
		return
	}
	iommuMap = make(map[string][]NvidiaPCIDevice)
	deviceMap = make(map[string][]string)
	nvSwitchDeviceIDs = make(map[string]bool)

	// Get all NVIDIA devices (GPUs and NVSwitches)
	devices, err := nvpciLib.GetAllDevices()
	if err != nil {
		log.Printf("Error discovering NVIDIA devices: %v", err)
		return
	}

	for _, dev := range devices {
		// Only process GPUs and NVSwitches
		if !dev.IsGPU() && !dev.IsNVSwitch() {
			continue
		}

		// Only process devices bound to vfio-pci driver
		if dev.Driver != "vfio-pci" {
			log.Printf("Skipping %s device %s: driver is %q, not vfio-pci",
				getDeviceType(dev), dev.Address, dev.Driver)
			continue
		}

		log.Printf("Found %s device %s (%s)", getDeviceType(dev), dev.Address, dev.DeviceName)

		// Determine IOMMU key (either IOMMU group or IOMMUFD device)
		iommuKey := strconv.Itoa(dev.IommuGroup)
		if iommufdSupported && dev.IommuFD != "" {
			iommuKey = dev.IommuFD
		}
		log.Printf("Iommu key (group/fd): %s", iommuKey)

		// Add to device map only for new IOMMU groups
		deviceID := fmt.Sprintf("%04x", dev.Device)
		if _, exists := iommuMap[iommuKey]; !exists {
			log.Printf("Device Id %s", deviceID)
			deviceMap[deviceID] = append(deviceMap[deviceID], iommuKey)
		}

		// Track NVSwitch device IDs
		isSwitch := dev.IsNVSwitch()
		if isSwitch {
			nvSwitchDeviceIDs[deviceID] = true
		}

		// Add device to IOMMU map
		iommuMap[iommuKey] = append(iommuMap[iommuKey], NvidiaPCIDevice{
			Address:    dev.Address,
			DeviceID:   dev.Device,
			DeviceName: dev.DeviceName,
			IommuGroup: dev.IommuGroup,
			IommuFD:    dev.IommuFD,
			IsNVSwitch: isSwitch,
		})
	}
}

// getDeviceType returns a human-readable device type string
func getDeviceType(dev *nvpci.NvidiaPCIDevice) string {
	if dev.IsNVSwitch() {
		return "NVSwitch"
	}
	return "GPU"
}

// isNVSwitchDeviceID returns true if the given device ID belongs to an NVSwitch
func isNVSwitchDeviceID(deviceID string) bool {
	return nvSwitchDeviceIDs[deviceID]
}

func getIommuMap() map[string][]NvidiaPCIDevice {
	return iommuMap
}

// getDeviceNameForID finds the device name for a given device ID from the discovered devices
func getDeviceNameForID(deviceID string) string {
	// Find the first device with this device ID in the iommu map
	for _, devices := range iommuMap {
		for _, dev := range devices {
			devIDStr := fmt.Sprintf("%04x", dev.DeviceID)
			if devIDStr == deviceID {
				return formatDeviceName(dev.DeviceName)
			}
		}
	}
	return ""
}

// formatDeviceName converts a device name to a Kubernetes-compatible resource name
func formatDeviceName(name string) string {
	if name == "" {
		return ""
	}
	// Convert to uppercase
	name = strings.ToUpper(name)
	// Replace / and . with underscore
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, ".", "_")
	// Replace all whitespace with underscore
	reg := regexp.MustCompile(`\s+`)
	name = reg.ReplaceAllString(name, "_")
	// Remove any characters that are not alphanumeric, underscore, or hyphen
	reg = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	name = reg.ReplaceAllString(name, "")
	return name
}
