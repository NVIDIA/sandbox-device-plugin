/*
 * Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package fabric_manager

import (
	"context"
	"fmt"
	"log"
	"sort"
)

// PartitionManager selects preferred device sets that exactly match fabric
// manager partitions. It translates plugin device IDs (IOMMU group/fd keys)
// to GPU physical module IDs via PCI BDF addresses, since FM partitions
// identify GPUs by physical module ID.
type PartitionManager struct {
	fm            FMClient
	pciToModule   map[string]uint32
	deviceIDToPCI map[string]string
}

// NewPartitionManager creates a PartitionManager. pciToModule maps normalized
// PCI BDF addresses to GPU physical module IDs (see LoadPCIModuleMapping) and
// deviceIDToPCI maps plugin device IDs to the PCI BDF address of the GPU they
// represent.
func NewPartitionManager(fm FMClient, pciToModule map[string]uint32, deviceIDToPCI map[string]string) *PartitionManager {
	normalizedPCIToModule := make(map[string]uint32, len(pciToModule))
	for addr, moduleID := range pciToModule {
		normalizedPCIToModule[NormalizePCIAddress(addr)] = moduleID
	}
	normalizedDeviceIDToPCI := make(map[string]string, len(deviceIDToPCI))
	for id, addr := range deviceIDToPCI {
		normalizedDeviceIDToPCI[id] = NormalizePCIAddress(addr)
	}
	return &PartitionManager{
		fm:            fm,
		pciToModule:   normalizedPCIToModule,
		deviceIDToPCI: normalizedDeviceIDToPCI,
	}
}

// physicalID translates a plugin device ID to a GPU physical module ID.
func (pm *PartitionManager) physicalID(deviceID string) (uint32, bool) {
	pciAddr, ok := pm.deviceIDToPCI[deviceID]
	if !ok {
		return 0, false
	}
	moduleID, ok := pm.pciToModule[pciAddr]
	return moduleID, ok
}

// SelectPreferred picks the devices that form a fabric partition of exactly
// the requested size, from the available device IDs, including all
// mustInclude device IDs. Partitions that are already active are preferred
// over inactive ones; ties are broken deterministically by lowest partition
// ID. It returns nil (with no error) when no partition fits, in which case
// the kubelet falls back to its default allocation. An error is only
// returned for fabric manager communication failures; callers should log it
// and treat it as "no preference" rather than failing the allocation.
func (pm *PartitionManager) SelectPreferred(ctx context.Context, availableDeviceIDs []string, mustInclude []string, size int) ([]string, error) {
	if size <= 0 || len(availableDeviceIDs) == 0 {
		return nil, nil
	}
	if len(mustInclude) > size {
		log.Printf("fabric_manager: %d must-include devices exceed allocation size %d, no partition preference", len(mustInclude), size)
		return nil, nil
	}

	// Translate available device IDs to physical module IDs.
	availablePhys := make(map[uint32]struct{}, len(availableDeviceIDs))
	physToDevice := make(map[uint32]string, len(availableDeviceIDs))
	for _, deviceID := range availableDeviceIDs {
		phys, ok := pm.physicalID(deviceID)
		if !ok {
			log.Printf("fabric_manager: skipping device %q: no PCI/module mapping", deviceID)
			continue
		}
		availablePhys[phys] = struct{}{}
		physToDevice[phys] = deviceID
	}
	if len(availablePhys) < size {
		log.Printf("fabric_manager: only %d/%d available devices mapped to physical IDs (need %d), no partition preference",
			len(availablePhys), len(availableDeviceIDs), size)
		return nil, nil
	}

	// Translate must-include device IDs; if any cannot be mapped we cannot
	// guarantee a partition contains it, so express no preference.
	mustPhys := make(map[uint32]struct{}, len(mustInclude))
	for _, deviceID := range mustInclude {
		phys, ok := pm.physicalID(deviceID)
		if !ok {
			log.Printf("fabric_manager: must-include device %q has no PCI/module mapping, no partition preference", deviceID)
			return nil, nil
		}
		mustPhys[phys] = struct{}{}
	}

	partitions, err := pm.fm.GetSupportedPartitions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get fabric partitions: %w", err)
	}

	// Collect partitions of exactly the requested size whose GPUs are all
	// available and that contain all must-include devices.
	var candidates []Partition
	for _, partition := range partitions {
		if len(partition.GPUPhysicalIDs) != size {
			continue
		}
		if !allInSet(partition.GPUPhysicalIDs, availablePhys) {
			continue
		}
		if !setInSlice(mustPhys, partition.GPUPhysicalIDs) {
			continue
		}
		candidates = append(candidates, partition)
	}
	if len(candidates) == 0 {
		log.Printf("fabric_manager: no fabric partition of size %d fits available devices %v (mustInclude %v), no partition preference",
			size, availableDeviceIDs, mustInclude)
		return nil, nil
	}

	// Prefer partitions that are already active, then lowest partition ID.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].IsActive != candidates[j].IsActive {
			return candidates[i].IsActive
		}
		return candidates[i].ID < candidates[j].ID
	})
	best := candidates[0]
	log.Printf("fabric_manager: selected partition %d (active: %t, GPUs: %v) from %d candidate(s)",
		best.ID, best.IsActive, best.GPUPhysicalIDs, len(candidates))

	// Build the preferred device list: must-include devices first (request
	// order), then the remaining partition members in partition order.
	preferred := make([]string, 0, size)
	added := make(map[string]struct{}, size)
	for _, deviceID := range mustInclude {
		if _, exists := added[deviceID]; exists {
			continue
		}
		added[deviceID] = struct{}{}
		preferred = append(preferred, deviceID)
	}
	for _, phys := range best.GPUPhysicalIDs {
		deviceID := physToDevice[phys]
		if _, exists := added[deviceID]; exists {
			continue
		}
		added[deviceID] = struct{}{}
		preferred = append(preferred, deviceID)
	}
	return preferred, nil
}

// allInSet reports whether every ID in ids is present in set.
func allInSet(ids []uint32, set map[uint32]struct{}) bool {
	for _, id := range ids {
		if _, ok := set[id]; !ok {
			return false
		}
	}
	return true
}

// setInSlice reports whether every ID in set is present in ids.
func setInSlice(set map[uint32]struct{}, ids []uint32) bool {
	present := make(map[uint32]struct{}, len(ids))
	for _, id := range ids {
		present[id] = struct{}{}
	}
	for id := range set {
		if _, ok := present[id]; !ok {
			return false
		}
	}
	return true
}
