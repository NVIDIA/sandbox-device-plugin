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
	"errors"
	"time"

	"github.com/NVIDIA/go-nvlib/pkg/nvpci"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func fakeStartDevicePluginFunc(dp *GenericDevicePlugin) error {
	if dp.deviceName == "1b80" {
		return errors.New("Incorrect operation")
	}
	return nil
}

var _ = Describe("Device Plugin", func() {
	Context("createIommuDeviceMap() Tests", func() {
		BeforeEach(func() {
			// Reset maps before each test
			iommuMap = nil
			deviceMap = nil
		})

		It("discovers GPUs bound to vfio-pci driver", func() {
			// Setup mock nvpci
			nvpciLib = &nvpci.InterfaceMock{
				GetAllDevicesFunc: func() ([]*nvpci.NvidiaPCIDevice, error) {
					return []*nvpci.NvidiaPCIDevice{
						{
							Address:    "0000:01:00.0",
							Vendor:     0x10de,
							Class:      nvpci.PCI3dControllerClass,
							Device:     0x1b80,
							DeviceName: "GeForce GTX 1080",
							Driver:     "vfio-pci",
							IommuGroup: 1,
						},
						{
							Address:    "0000:02:00.0",
							Vendor:     0x10de,
							Class:      nvpci.PCI3dControllerClass,
							Device:     0x1b81,
							DeviceName: "GeForce GTX 1070",
							Driver:     "vfio-pci",
							IommuGroup: 2,
						},
					}, nil
				},
			}
			startDevicePlugin = fakeStartDevicePluginFunc

			createIommuDeviceMap()

			Expect(iommuMap).To(HaveLen(2))
			Expect(iommuMap["1"]).To(HaveLen(1))
			Expect(iommuMap["1"][0].Address).To(Equal("0000:01:00.0"))
			Expect(iommuMap["2"]).To(HaveLen(1))
			Expect(iommuMap["2"][0].Address).To(Equal("0000:02:00.0"))

			Expect(deviceMap).To(HaveLen(2))
			Expect(deviceMap["1b80"]).To(ContainElement("1"))
			Expect(deviceMap["1b81"]).To(ContainElement("2"))
		})

		It("discovers NVSwitches bound to vfio-pci driver", func() {
			nvpciLib = &nvpci.InterfaceMock{
				GetAllDevicesFunc: func() ([]*nvpci.NvidiaPCIDevice, error) {
					return []*nvpci.NvidiaPCIDevice{
						{
							Address:    "0000:03:00.0",
							Vendor:     0x10de,
							Class:      nvpci.PCINvSwitchClass,
							Device:     0x2000,
							DeviceName: "NVSwitch",
							Driver:     "vfio-pci",
							IommuGroup: 3,
						},
					}, nil
				},
			}

			createIommuDeviceMap()

			Expect(iommuMap).To(HaveLen(1))
			Expect(iommuMap["3"]).To(HaveLen(1))
			Expect(iommuMap["3"][0].Address).To(Equal("0000:03:00.0"))
			Expect(deviceMap["2000"]).To(ContainElement("3"))
		})

		It("skips devices not bound to vfio-pci driver", func() {
			nvpciLib = &nvpci.InterfaceMock{
				GetAllDevicesFunc: func() ([]*nvpci.NvidiaPCIDevice, error) {
					return []*nvpci.NvidiaPCIDevice{
						{
							Address:    "0000:01:00.0",
							Vendor:     0x10de,
							Class:      nvpci.PCI3dControllerClass,
							Device:     0x1b80,
							DeviceName: "GeForce GTX 1080",
							Driver:     "nvidia",
							IommuGroup: 1,
						},
					}, nil
				},
			}

			createIommuDeviceMap()

			Expect(iommuMap).To(BeEmpty())
			Expect(deviceMap).To(BeEmpty())
		})

		It("skips non-GPU non-NVSwitch devices", func() {
			nvpciLib = &nvpci.InterfaceMock{
				GetAllDevicesFunc: func() ([]*nvpci.NvidiaPCIDevice, error) {
					return []*nvpci.NvidiaPCIDevice{
						{
							Address:    "0000:01:00.0",
							Vendor:     0x10de,
							Class:      0x020000, // Network controller
							Device:     0x1234,
							DeviceName: "Network Device",
							Driver:     "vfio-pci",
							IommuGroup: 1,
						},
					}, nil
				},
			}

			createIommuDeviceMap()

			Expect(iommuMap).To(BeEmpty())
			Expect(deviceMap).To(BeEmpty())
		})

		It("creates device plugins for discovered devices", func() {
			nvpciLib = &nvpci.InterfaceMock{
				GetAllDevicesFunc: func() ([]*nvpci.NvidiaPCIDevice, error) {
					return []*nvpci.NvidiaPCIDevice{
						{
							Address:    "0000:01:00.0",
							Vendor:     0x10de,
							Class:      nvpci.PCI3dControllerClass,
							Device:     0x1b81,
							DeviceName: "GeForce GTX 1070",
							Driver:     "vfio-pci",
							IommuGroup: 1,
						},
					}, nil
				},
			}
			startDevicePlugin = fakeStartDevicePluginFunc

			createIommuDeviceMap()

			go createDevicePlugins()
			time.Sleep(100 * time.Millisecond)
			stop <- struct{}{}
		})

		It("tracks NVSwitch device IDs separately", func() {
			nvpciLib = &nvpci.InterfaceMock{
				GetAllDevicesFunc: func() ([]*nvpci.NvidiaPCIDevice, error) {
					return []*nvpci.NvidiaPCIDevice{
						{
							Address:    "0000:01:00.0",
							Vendor:     0x10de,
							Class:      nvpci.PCI3dControllerClass,
							Device:     0x1b80,
							DeviceName: "GeForce GTX 1080",
							Driver:     "vfio-pci",
							IommuGroup: 1,
						},
						{
							Address:    "0000:03:00.0",
							Vendor:     0x10de,
							Class:      nvpci.PCINvSwitchClass,
							Device:     0x2000,
							DeviceName: "NVSwitch",
							Driver:     "vfio-pci",
							IommuGroup: 3,
						},
					}, nil
				},
			}

			createIommuDeviceMap()

			// GPU should not be tracked as NVSwitch
			Expect(isNVSwitchDeviceID("1b80")).To(BeFalse())
			// NVSwitch should be tracked
			Expect(isNVSwitchDeviceID("2000")).To(BeTrue())
			// Device in iommuMap should have IsNVSwitch set correctly
			Expect(iommuMap["1"][0].IsNVSwitch).To(BeFalse())
			Expect(iommuMap["3"][0].IsNVSwitch).To(BeTrue())
		})
	})

	Context("formatDeviceName() Tests", func() {
		It("converts device name to uppercase", func() {
			result := formatDeviceName("geforce gtx 1080")
			Expect(result).To(Equal("GEFORCE_GTX_1080"))
		})

		It("replaces / and . with underscore", func() {
			result := formatDeviceName("GK104.GL [GRID/K520]")
			Expect(result).To(Equal("GK104_GL_GRID_K520"))
		})

		It("replaces multiple spaces with single underscore", func() {
			result := formatDeviceName("GeForce   GTX  1080")
			Expect(result).To(Equal("GEFORCE_GTX_1080"))
		})

		It("removes non-alphanumeric characters except underscore and hyphen", func() {
			result := formatDeviceName("Device [Name] (Rev A)")
			Expect(result).To(Equal("DEVICE_NAME_REV_A"))
		})

		It("returns empty string for empty input", func() {
			result := formatDeviceName("")
			Expect(result).To(Equal(""))
		})
	})

	Context("getDeviceNameForID() Tests", func() {
		BeforeEach(func() {
			// Setup test data in iommuMap
			iommuMap = map[string][]NvidiaPCIDevice{
				"1": {
					{
						Address:    "0000:01:00.0",
						DeviceID:   0x1b80,
						DeviceName: "GeForce GTX 1080",
						IommuGroup: 1,
					},
				},
				"2": {
					{
						Address:    "0000:02:00.0",
						DeviceID:   0x1b81,
						DeviceName: "GeForce GTX 1070",
						IommuGroup: 2,
					},
				},
			}
		})

		It("returns formatted device name for existing device ID", func() {
			result := getDeviceNameForID("1b80")
			Expect(result).To(Equal("GEFORCE_GTX_1080"))
		})

		It("returns empty string for non-existent device ID", func() {
			result := getDeviceNameForID("abcd")
			Expect(result).To(Equal(""))
		})

		It("formats device names with special characters through formatDeviceName", func() {
			iommuMap = map[string][]NvidiaPCIDevice{
				"1": {
					{
						Address:    "0000:01:00.0",
						DeviceID:   0x2330,
						DeviceName: "NVIDIA H100 PCIe [Hopper]",
						IommuGroup: 1,
					},
				},
			}
			result := getDeviceNameForID("2330")
			Expect(result).To(Equal("NVIDIA_H100_PCIE_HOPPER"))
		})

		It("returns empty string when iommuMap is empty", func() {
			iommuMap = map[string][]NvidiaPCIDevice{}
			result := getDeviceNameForID("1b80")
			Expect(result).To(Equal(""))
		})
	})
})
