/*
 * Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/go-nvlib/pkg/nvpci"
	"github.com/fsnotify/fsnotify"
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
	log.Printf("iommufd supported: %v", iommufdSupported)
	log.Printf("Device map: %v", deviceMap)

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

		log.Printf("Registering device plugin %q with %d device(s)", deviceName, len(devs))
		devicePath := "/dev/vfio/"
		if iommufdSupported {
			devicePath = "/dev/vfio/devices/"
		}
		dp := NewGenericDevicePlugin(deviceName, devicePath, devs)
		if err := startDevicePlugin(dp); err != nil {
			log.Printf("Error starting %s device plugin: %v (will retry)", dp.deviceName, err)
		}
		// Track the plugin even if it failed to start, so that runPluginManager
		// retries it rather than leaving the resource permanently unadvertised.
		devicePlugins = append(devicePlugins, dp)
	}

	// run GFD job
	if EnableGFD {
		go runGFD()
	}

	runPluginManager(devicePlugins)
}

// runPluginManager blocks until shutdown, keeping every device plugin
// registered with kubelet.
//
// Kubelet wipes /var/lib/kubelet/device-plugins when it initializes, deleting
// every plugin socket in it. If this process starts before kubelet finishes
// initializing -- which happens on a cold boot -- it registers successfully
// against the outgoing kubelet.sock and is then silently unregistered when
// kubelet wipes the directory. The process stays alive serving gRPC on an
// unlinked inode and the node advertises zero devices until someone deletes
// the pod by hand.
//
// Two signals bring the plugins back:
//
//   - a CREATE event for kubelet.sock in the device-plugin directory, which is
//     the standard device-plugin API contract for "kubelet restarted"; and
//   - a periodic check that each plugin's own socket is still on disk, which
//     covers a socket removed without kubelet.sock being recreated, a missed
//     or undeliverable fsnotify event, and retrying a failed re-registration.
//
// Nothing here requires a particular resource to exist: plugins are whatever
// was discovered on this node, so a node with no NVSwitch simply has no
// nvswitch plugin to watch.
func runPluginManager(plugins []*GenericDevicePlugin) {
	var events chan fsnotify.Event
	var watchErrors chan error

	watcher, err := newDirWatcher(devicePluginPath)
	if err != nil {
		log.Printf("Unable to watch %s (%v); falling back to the %s socket check to detect kubelet restarts",
			devicePluginPath, err, socketCheckInterval)
	} else {
		defer watcher.Close()
		events, watchErrors = watcher.Events, watcher.Errors
		log.Printf("Watching %s for kubelet restarts", devicePluginPath)
	}

	ticker := time.NewTicker(socketCheckInterval)
	defer ticker.Stop()

	// Identity of the kubelet.sock the plugins are registered against. A
	// kubelet restart replaces that inode; re-registering ourselves does not.
	// Comparing inodes keeps a duplicate or spurious CREATE event -- our own
	// socket churn can produce one, depending on the fsnotify backend -- from
	// putting the plugins into a re-registration loop.
	currentKubeletSocket, _ := os.Stat(kubeletSocket)

	reregister := func(targets []*GenericDevicePlugin) {
		restartPlugins(targets)
		if fi, err := os.Stat(kubeletSocket); err == nil {
			currentKubeletSocket = fi
		}
	}

	for {
		select {
		case <-stop:
			log.Printf("Shutting down device plugin controller")
			for _, dp := range plugins {
				dp.Stop()
			}
			return

		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event.Name != kubeletSocket || !event.Has(fsnotify.Create) {
				continue
			}
			fi, err := os.Stat(kubeletSocket)
			if err != nil {
				log.Printf("Got a create event for %s but could not stat it: %v", kubeletSocket, err)
				continue
			}
			if currentKubeletSocket != nil && os.SameFile(currentKubeletSocket, fi) {
				// Same socket we are already registered against.
				continue
			}
			currentKubeletSocket = fi
			log.Printf("%s was recreated, kubelet restarted; re-registering all device plugins",
				kubeletSocket)
			reregister(plugins)

		case err, ok := <-watchErrors:
			if !ok {
				watchErrors = nil
				continue
			}
			log.Printf("fsnotify error while watching %s: %v", devicePluginPath, err)

		case <-ticker.C:
			var lost []*GenericDevicePlugin
			for _, dp := range plugins {
				if !dp.registered() {
					log.Printf("Device plugin %q is not registered with kubelet, re-registering it",
						dp.deviceName)
					lost = append(lost, dp)
				}
			}
			if len(lost) > 0 {
				reregister(lost)
			}
		}
	}
}

// restartPlugins re-registers each plugin. Failures are logged and left for
// the next socket check to retry; one plugin failing must not stop the others.
func restartPlugins(plugins []*GenericDevicePlugin) {
	for _, dp := range plugins {
		if err := dp.restart(); err != nil {
			log.Printf("Failed to re-register %s device plugin: %v (will retry in %s)",
				dp.deviceName, err, socketCheckInterval)
			continue
		}
		log.Printf("Re-registered device plugin %q with %d device(s)", dp.deviceName, len(dp.devs))
	}
}

// newDirWatcher returns an fsnotify watcher already watching dir, or an error
// with the watcher cleaned up.
func newDirWatcher(dir string) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, err
	}
	return watcher, nil
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

		// Determine IOMMU key (either IOMMU group or IOMMUFD device number).
		// dev.IommuFD is "vfio<NUM>" but we strip the prefix so the key is
		// just the number, consistent with the legacy IOMMU group key and
		// the VFIO device paths used in CDI specs and allocation.
		iommuKey := strconv.Itoa(dev.IommuGroup)
		if iommufdSupported && dev.IommuFD != "" {
			iommuKey = strings.TrimPrefix(dev.IommuFD, "vfio")
		}

		deviceID := fmt.Sprintf("%04x", dev.Device)
		if iommufdSupported {
			log.Printf("Found %s %s [%s] at %s (iommufd: %s)",
				getDeviceType(dev), dev.DeviceName, deviceID, dev.Address, dev.IommuFD)
		} else {
			log.Printf("Found %s %s [%s] at %s (vfio group: %d)",
				getDeviceType(dev), dev.DeviceName, deviceID, dev.Address, dev.IommuGroup)
		}

		// Add to device map only for new IOMMU groups
		if _, exists := iommuMap[iommuKey]; !exists {
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
