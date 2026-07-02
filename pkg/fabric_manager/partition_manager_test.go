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

package fabric_manager_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	fm "github.com/nvidia/sandbox-device-plugin/pkg/fabric_manager"
)

// fakeFMClient is a fake FMClient returning a fixed partition list.
type fakeFMClient struct {
	partitions []fm.Partition
	err        error
}

func (f *fakeFMClient) GetSupportedPartitions(ctx context.Context) ([]fm.Partition, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.partitions, nil
}

var _ = Describe("PartitionManager", func() {
	// H100 HGX topology: 8 GPUs with physical module IDs 1..8. Plugin device
	// IDs are iommufd numbers "0".."7"; deviceID "N" maps to physical ID N+1.
	deviceIDToPCI := map[string]string{
		"0": "0000:19:00.0",
		"1": "0000:3b:00.0",
		"2": "0000:4c:00.0",
		"3": "0000:5d:00.0",
		"4": "0000:9b:00.0",
		"5": "0000:bb:00.0",
		"6": "0000:cb:00.0",
		"7": "0000:db:00.0",
	}
	pciToModule := map[string]uint32{
		"0000:19:00.0": 1,
		"0000:3b:00.0": 2,
		"0000:4c:00.0": 3,
		"0000:5d:00.0": 4,
		"0000:9b:00.0": 5,
		"0000:bb:00.0": 6,
		"0000:cb:00.0": 7,
		"0000:db:00.0": 8,
	}
	allDevices := []string{"0", "1", "2", "3", "4", "5", "6", "7"}

	// H100 fabric partition table: one 8-GPU, two 4-GPU, four 2-GPU
	// "diagonal" pairs and eight singles. Listed in descending-ID order to
	// verify selection does not depend on FM list order.
	h100Partitions := func() []fm.Partition {
		partitions := []fm.Partition{
			{ID: 6, GPUPhysicalIDs: []uint32{6, 8}},
			{ID: 5, GPUPhysicalIDs: []uint32{5, 7}},
			{ID: 4, GPUPhysicalIDs: []uint32{2, 4}},
			{ID: 3, GPUPhysicalIDs: []uint32{1, 3}},
			{ID: 2, GPUPhysicalIDs: []uint32{5, 6, 7, 8}},
			{ID: 1, GPUPhysicalIDs: []uint32{1, 2, 3, 4}},
			{ID: 0, GPUPhysicalIDs: []uint32{1, 2, 3, 4, 5, 6, 7, 8}},
		}
		for i := uint32(1); i <= 8; i++ {
			partitions = append(partitions, fm.Partition{ID: 6 + i, GPUPhysicalIDs: []uint32{i}})
		}
		return partitions
	}

	newManager := func(partitions []fm.Partition) *fm.PartitionManager {
		return fm.NewPartitionManager(&fakeFMClient{partitions: partitions}, pciToModule, deviceIDToPCI)
	}

	Context("SelectPreferred() with the H100 partition table", func() {
		DescribeTable("selects a full partition of the requested size",
			func(available []string, mustInclude []string, size int, expected []string) {
				pm := newManager(h100Partitions())
				preferred, err := pm.SelectPreferred(context.Background(), available, mustInclude, size)
				Expect(err).ToNot(HaveOccurred())
				Expect(preferred).To(Equal(expected))
			},
			// size=2 must pick a diagonal NVLink pair, never adjacent {1,2}:
			// lowest 2-GPU partition ID is 3 = physical {1,3} = devices {0,2}.
			Entry("size 2 picks a diagonal pair, not {1,2}",
				allDevices, nil, 2, []string{"0", "2"}),
			Entry("size 4 picks the first 4-GPU partition",
				allDevices, nil, 4, []string{"0", "1", "2", "3"}),
			Entry("size 8 picks the full 8-GPU partition",
				allDevices, nil, 8, allDevices),
			Entry("size 1 picks a single-GPU partition",
				allDevices, nil, 1, []string{"0"}),
			// mustInclude device "1" (physical 2) forces partition {2,4}.
			Entry("mustInclude steers to the matching pair",
				allDevices, []string{"1"}, 2, []string{"1", "3"}),
			// Fragmentation: devices 0,1 (physical 1,2) already taken; the
			// only fully-available pairs are {5,7} and {6,8}; lowest ID wins.
			Entry("fragmentation picks a fully-available pair",
				[]string{"2", "3", "4", "5", "6", "7"}, nil, 2, []string{"4", "6"}),
			Entry("fragmentation picks the fully-available 4-GPU partition",
				[]string{"2", "3", "4", "5", "6", "7"}, nil, 4, []string{"4", "5", "6", "7"}),
		)

		DescribeTable("returns nil when no partition fits",
			func(available []string, mustInclude []string, size int) {
				pm := newManager(h100Partitions())
				preferred, err := pm.SelectPreferred(context.Background(), available, mustInclude, size)
				Expect(err).ToNot(HaveOccurred())
				Expect(preferred).To(BeNil())
			},
			Entry("no partition of size 3 exists", allDevices, nil, 3),
			Entry("no fully-available pair remains",
				[]string{"0", "1", "4", "5"}, nil, 4),
			// mustInclude devices "0","1" (physical 1,2) never share a pair.
			Entry("mustInclude devices not in any pair together",
				allDevices, []string{"0", "1"}, 2),
			Entry("mustInclude exceeds allocation size", allDevices, []string{"0", "2", "4"}, 2),
			Entry("size 0 yields no preference", allDevices, nil, 0),
			Entry("no available devices", []string{}, nil, 2),
		)

		It("prefers a partition that is already active", func() {
			partitions := h100Partitions()
			for i := range partitions {
				if partitions[i].ID == 6 { // physical {6,8} = devices {5,7}
					partitions[i].IsActive = true
				}
			}
			pm := newManager(partitions)
			preferred, err := pm.SelectPreferred(context.Background(), allDevices, nil, 2)
			Expect(err).ToNot(HaveOccurred())
			Expect(preferred).To(Equal([]string{"5", "7"}))
		})

		It("tie-breaks equal-priority partitions by lowest partition ID", func() {
			partitions := h100Partitions()
			for i := range partitions {
				if partitions[i].ID == 4 || partitions[i].ID == 5 {
					partitions[i].IsActive = true
				}
			}
			pm := newManager(partitions)
			preferred, err := pm.SelectPreferred(context.Background(), allDevices, nil, 2)
			Expect(err).ToNot(HaveOccurred())
			// Both {2,4} (ID 4) and {5,7} (ID 5) are active; ID 4 wins.
			Expect(preferred).To(Equal([]string{"1", "3"}))
		})

		It("skips unmappable available devices and still selects a partition", func() {
			pm := newManager(h100Partitions())
			available := append([]string{"unknown-device"}, allDevices...)
			preferred, err := pm.SelectPreferred(context.Background(), available, nil, 2)
			Expect(err).ToNot(HaveOccurred())
			Expect(preferred).To(Equal([]string{"0", "2"}))
		})

		It("returns nil when a must-include device is unmappable", func() {
			pm := newManager(h100Partitions())
			preferred, err := pm.SelectPreferred(context.Background(), allDevices, []string{"unknown-device"}, 2)
			Expect(err).ToNot(HaveOccurred())
			Expect(preferred).To(BeNil())
		})

		It("returns an error when fabric manager is unreachable", func() {
			pm := fm.NewPartitionManager(&fakeFMClient{err: errors.New("connection refused")}, pciToModule, deviceIDToPCI)
			preferred, err := pm.SelectPreferred(context.Background(), allDevices, nil, 2)
			Expect(err).To(HaveOccurred())
			Expect(preferred).To(BeNil())
		})
	})

	Context("NewPartitionManager()", func() {
		It("normalizes PCI addresses from both maps", func() {
			pm := fm.NewPartitionManager(
				&fakeFMClient{partitions: []fm.Partition{{ID: 1, GPUPhysicalIDs: []uint32{1, 2}}}},
				map[string]uint32{"00000000:19:00.0": 1, "0000:3B:00.0": 2},
				map[string]string{"0": "0000:19:00.0", "1": "00000000:3b:00.0"},
			)
			preferred, err := pm.SelectPreferred(context.Background(), []string{"0", "1"}, nil, 2)
			Expect(err).ToNot(HaveOccurred())
			Expect(preferred).To(Equal([]string{"0", "1"}))
		})
	})
})
