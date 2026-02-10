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
// GPUs use the PGPUAlias (or cdiGPUClass if not set) as the class name.
// NVSwitches always use cdiNVSwitchClass as the class name.
func GenerateCDISpec() error {
	if len(iommuMap) == 0 {
		log.Printf("No devices discovered, skipping CDI spec generation")
		return nil
	}

	// Ensure CDI directory exists
	if err := os.MkdirAll(cdiRoot, 0755); err != nil {
		return fmt.Errorf("failed to create CDI directory %s: %w", cdiRoot, err)
	}

	// Determine GPU class name
	gpuClass := cdiGPUClass
	if PGPUAlias != "" {
		gpuClass = PGPUAlias
	}

	// Generate GPU CDI spec
	if err := generateCDISpecForClass(gpuClass, false); err != nil {
		log.Println(err.Error())
		return fmt.Errorf("failed to generate GPU CDI spec: %w", err)
	}

	// Generate NVSwitch CDI spec if we have NVSwitch devices
	if len(nvSwitchDeviceIDs) > 0 {
		nvSwitchClass := cdiNVSwitchClass
		if NVSwitchAlias != "" {
			nvSwitchClass = NVSwitchAlias
		}
		if err := generateCDISpecForClass(nvSwitchClass, true); err != nil {
			log.Println(err.Error())
			return fmt.Errorf("failed to generate NVSwitch CDI spec: %w", err)
		}
	}

	return nil
}

// generateCDISpecForClass generates a CDI spec for either GPUs or NVSwitches.
// The CDI spec allows container runtimes to inject VFIO devices into containers
// without requiring privileged mode. Each device entry maps to a VFIO device
// that can be requested by name (e.g., "nvidia.com/pgpu=0").
func generateCDISpecForClass(class string, isNVSwitch bool) error {
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
	iommuKeys := make([]string, 0, len(iommuMap))
	for k := range iommuMap {
		iommuKeys = append(iommuKeys, k)
	}
	sort.Slice(iommuKeys, func(i, j int) bool {
		return extractNumber(iommuKeys[i]) < extractNumber(iommuKeys[j])
	})

	for _, iommuKey := range iommuKeys {
		devices := iommuMap[iommuKey]
		for _, dev := range devices {
			// Filter devices by type - generate separate specs for GPUs and NVSwitches
			if dev.IsNVSwitch != isNVSwitch {
				continue
			}

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
