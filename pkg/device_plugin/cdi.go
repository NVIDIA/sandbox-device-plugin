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
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	"tags.cncf.io/container-device-interface/specs-go"
)

const (
	kataCompatibleCDIVersion = "0.5.0"
)

// GenerateCDISpec generates CDI specifications for discovered VFIO devices.
//
// Both GPUs and NVSwitches follow the same alias logic:
//
// When the alias is set (P_GPU_ALIAS / NVSWITCH_ALIAS), all devices of that
// category are combined into a single CDI spec under the alias (e.g.,
// "nvidia.com/pgpu", "nvidia.com/nvswitch").
//
// When the alias is not set, each device type gets its own CDI spec using
// the formatted device name as the class (e.g.,
// "nvidia.com/GH100_H100_SXM5_80GB", "nvidia.com/GH100_H100_NVSWITCH").
func GenerateCDISpec() error {
	if len(iommuMap) == 0 {
		log.Printf("No devices discovered, skipping CDI spec generation")
		return nil
	}

	// Ensure CDI directory exists
	if err := os.MkdirAll(cdiRoot, 0755); err != nil {
		return fmt.Errorf("failed to create CDI directory %s: %w", cdiRoot, err)
	}

	if PGPUAlias != "" {
		// Homogeneous mode: all GPUs in one CDI spec under the alias
		var gpuKeys []string
		for deviceID, keys := range deviceMap {
			if isNVSwitchDeviceID(deviceID) {
				continue
			}
			gpuKeys = append(gpuKeys, keys...)
		}
		if len(gpuKeys) > 0 {
			if err := generateCDISpecForClass(PGPUAlias, gpuKeys); err != nil {
				log.Println(err.Error())
				return fmt.Errorf("failed to generate GPU CDI spec: %w", err)
			}
		}
	} else {
		// Heterogeneous mode: one CDI spec per GPU device type
		for deviceID, keys := range deviceMap {
			if isNVSwitchDeviceID(deviceID) {
				continue
			}
			className := getDeviceNameForID(deviceID)
			if className == "" {
				className = deviceID
			}
			if err := generateCDISpecForClass(className, keys); err != nil {
				log.Println(err.Error())
				return fmt.Errorf("failed to generate CDI spec for %s: %w", className, err)
			}
		}
	}

	// Generate NVSwitch CDI specs — same logic as GPUs:
	// alias set = all NVSwitches in one spec, alias unset = per device type
	if NVSwitchAlias != "" {
		var nvSwitchKeys []string
		for deviceID, keys := range deviceMap {
			if isNVSwitchDeviceID(deviceID) {
				nvSwitchKeys = append(nvSwitchKeys, keys...)
			}
		}
		if len(nvSwitchKeys) > 0 {
			if err := generateCDISpecForClass(NVSwitchAlias, nvSwitchKeys); err != nil {
				log.Println(err.Error())
				return fmt.Errorf("failed to generate NVSwitch CDI spec: %w", err)
			}
		}
	} else {
		for deviceID, keys := range deviceMap {
			if !isNVSwitchDeviceID(deviceID) {
				continue
			}
			className := getDeviceNameForID(deviceID)
			if className == "" {
				className = deviceID
			}
			if err := generateCDISpecForClass(className, keys); err != nil {
				log.Println(err.Error())
				return fmt.Errorf("failed to generate CDI spec for %s: %w", className, err)
			}
		}
	}

	return nil
}

// generateCDISpecForClass generates a CDI spec for the given class using the
// specified IOMMU keys. The CDI spec allows container runtimes to inject VFIO
// devices into containers without requiring privileged mode. Each device entry
// maps to a VFIO device that can be requested by name (e.g., "nvidia.com/pgpu=0").
func generateCDISpecForClass(class string, scopedIommuKeys []string) error {
	var deviceSpecs []specs.Device
	idx := 0

	iommufdSupported, err := supportsIOMMUFD()
	if err != nil {
		return fmt.Errorf("failed to check IOMMUFD support: %w", err)
	}

	// Sort iommu keys to ensure deterministic device ordering in the CDI spec.
	// Go maps have random iteration order, so without sorting the device names
	// (0, 1, 2...) would not correspond to ascending VFIO device numbers.
	// Keys can be either IOMMU group numbers ("8", "9", "10") or IOMMUFD device
	// names ("vfio8", "vfio9", "vfio10"). We sort numerically by extracting the
	// number, since lexicographic sort would put "10" before "8".
	sortedKeys := make([]string, len(scopedIommuKeys))
	copy(sortedKeys, scopedIommuKeys)
	sort.Slice(sortedKeys, func(i, j int) bool {
		return extractNumber(sortedKeys[i]) < extractNumber(sortedKeys[j])
	})

	for _, iommuKey := range sortedKeys {
		devices := iommuMap[iommuKey]
		for _, dev := range devices {
			// Build the device node paths based on IOMMU mode:
			// - IOMMUFD (modern): single device at /dev/vfio/devices/<fd>
			// - Legacy VFIO: requires both /dev/vfio/vfio (control) and /dev/vfio/<group>
			var deviceNodes []*specs.DeviceNode
			if iommufdSupported && dev.IommuFD != "" {
				deviceNodes = append(deviceNodes, &specs.DeviceNode{
					Path: filepath.Join(vfioDevicePath, "devices", dev.IommuFD),
				})
			} else {
				deviceNodes = append(deviceNodes,
					&specs.DeviceNode{
						Path: filepath.Join(vfioDevicePath, "vfio"),
					},
					&specs.DeviceNode{
						Path: filepath.Join(vfioDevicePath, iommuKey),
					},
				)
			}

			cedits := specs.ContainerEdits{
				DeviceNodes: deviceNodes,
			}

			deviceSpecs = append(deviceSpecs, specs.Device{
				Name:           fmt.Sprintf("%d", idx),
				ContainerEdits: cedits,
			})

			log.Printf("Added CDI device %d: address=%s, iommu=%s, class=%s",
				idx, dev.Address, iommuKey, class)

			idx++
		}
	}

	if len(deviceSpecs) == 0 {
		log.Printf("No %s devices found for CDI spec", class)
		return nil
	}

	// Create the CDI spec with vendor/class format (e.g., "nvidia.com/pgpu")
	spec := &specs.Spec{
		Version: kataCompatibleCDIVersion,
		Kind:    fmt.Sprintf("%s/%s", cdiVendor, class),
		Devices: deviceSpecs,
	}

	// Generate a unique spec name based on vendor and class
	specName, err := cdiapi.GenerateNameForSpec(spec)
	if err != nil {
		return fmt.Errorf("failed to generate CDI spec name: %w", err)
	}

	// Use CDI cache to write the spec - this handles file creation and formatting
	cache, err := cdiapi.NewCache(cdiapi.WithSpecDirs(cdiRoot))
	if err != nil {
		return fmt.Errorf("failed to create CDI cache: %w", err)
	}

	if err := cache.WriteSpec(spec, specName); err != nil {
		return fmt.Errorf("failed to save CDI spec %s: %w", specName, err)
	}

	log.Printf("Generated CDI spec: %s with %d devices", specName, len(deviceSpecs))
	return nil
}

// extractNumber extracts the numeric portion from an IOMMU key for sorting.
// Handles both pure numbers ("8") and prefixed names ("vfio8").
func extractNumber(s string) int {
	// Strip any non-digit prefix (e.g., "vfio" from "vfio8")
	numStr := strings.TrimLeft(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0
	}
	return n
}
