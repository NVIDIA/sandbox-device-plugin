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
	"os"
	"path"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

var devices []*pluginapi.Device
var iommuGroup1 = "1"
var iommuGroup2 = "2"
var iommuGroup3 = "3"
var iommuGroup4 = "4"
var pciAddress1 = "0000:01:00.0"
var pciAddress2 = "0000:02:00.0"
var pciAddress3 = "0000:03:00.0"

type fakeDevicePluginListAndWatchServer struct {
	grpc.ServerStream
}

func (x *fakeDevicePluginListAndWatchServer) Send(m *pluginapi.ListAndWatchResponse) error {
	devices = m.Devices
	return nil
}

func getFakeIommuMap() map[string][]NvidiaPCIDevice {
	var tempMap = make(map[string][]NvidiaPCIDevice)
	tempMap[iommuGroup1] = append(tempMap[iommuGroup1], NvidiaPCIDevice{
		Address:    pciAddress1,
		DeviceID:   0x1b80,
		DeviceName: "GeForce GTX 1080",
		IommuGroup: 1,
		IommuFD:    "vfio3",
	})
	tempMap[iommuGroup2] = append(tempMap[iommuGroup2], NvidiaPCIDevice{
		Address:    pciAddress2,
		DeviceID:   0x1b81,
		DeviceName: "GeForce GTX 1070",
		IommuGroup: 2,
		IommuFD:    "vfio4",
	})
	tempMap[iommuGroup3] = append(tempMap[iommuGroup3], NvidiaPCIDevice{
		Address:    pciAddress3,
		DeviceID:   0x1b82,
		DeviceName: "GeForce GTX 1060",
		IommuGroup: 3,
		IommuFD:    "", // No IOMMUFD for this device
	})
	return tempMap
}

var _ = Describe("Generic Device", func() {
	var workDir string
	var err error
	var dpi *GenericDevicePlugin
	var stop chan struct{}
	var devicePath string

	BeforeEach(func() {
		returnIommuMap = getFakeIommuMap
		var devs []*pluginapi.Device
		workDir, err = os.MkdirTemp("", "kubevirt-test")
		Expect(err).ToNot(HaveOccurred())
		rootPath = workDir

		devicePath = path.Join(workDir, iommuGroup1)
		fileObj, err := os.Create(devicePath)
		Expect(err).ToNot(HaveOccurred())
		fileObj.Close()

		devicePath = path.Join(workDir, iommuGroup2)
		fileObj, err = os.Create(devicePath)
		Expect(err).ToNot(HaveOccurred())
		fileObj.Close()

		devs = append(devs, &pluginapi.Device{
			ID:     iommuGroup1,
			Health: pluginapi.Healthy,
		})
		devs = append(devs, &pluginapi.Device{
			ID:     iommuGroup2,
			Health: pluginapi.Healthy,
		})
		dpi = NewGenericDevicePlugin("foo", workDir+"/", devs)
		stop = make(chan struct{})
		dpi.stop = stop
	})

	AfterEach(func() {
		close(stop)
		os.RemoveAll(workDir)
	})

	It("Should register a new device without error", func() {
		err := dpi.Stop()

		Expect(err).To(BeNil())
	})

	It("Should allocate a device without error", func() {
		devs := []string{iommuGroup1}
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, err := dpi.Allocate(ctx, &requests)
		Expect(err).To(BeNil())
		Expect(responses.GetContainerResponses()[0].Envs).To(BeNil())
		Expect(responses.GetContainerResponses()[0].Devices[0].HostPath).To(Equal("/dev/vfio/vfio"))
		Expect(responses.GetContainerResponses()[0].Devices[0].ContainerPath).To(Equal("/dev/vfio/vfio"))
		Expect(responses.GetContainerResponses()[0].Devices[0].Permissions).To(Equal("mrw"))
		Expect(responses.GetContainerResponses()[0].Devices[1].HostPath).To(Equal("/dev/vfio/1"))
		Expect(responses.GetContainerResponses()[0].Devices[1].ContainerPath).To(Equal("/dev/vfio/1"))
		Expect(responses.GetContainerResponses()[0].Devices[1].Permissions).To(Equal("mrw"))
	})

	It("Should allocate a device without error with iommufd support", func() {
		Expect(os.MkdirAll(filepath.Join(workDir, "dev"), 0744)).To(Succeed())
		f, err := os.OpenFile(filepath.Join(workDir, "dev", "iommu"), os.O_RDONLY|os.O_CREATE, 0666)
		Expect(err).ToNot(HaveOccurred())
		f.Close()

		devs := []string{iommuGroup1}
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, err := dpi.Allocate(ctx, &requests)
		Expect(err).To(BeNil())
		Expect(responses.GetContainerResponses()[0].Envs).To(BeNil())
		Expect(responses.GetContainerResponses()[0].Devices[0].HostPath).To(Equal("/dev/vfio/devices/vfio3"))
		Expect(responses.GetContainerResponses()[0].Devices[0].ContainerPath).To(Equal("/dev/vfio/devices/vfio3"))
		Expect(responses.GetContainerResponses()[0].Devices[0].Permissions).To(Equal("mrw"))
		Expect(len(responses.GetContainerResponses()[0].Devices)).To(Equal(1))
	})

	It("Should fail allocation when iommufd is supported but device has no IommuFD", func() {
		Expect(os.MkdirAll(filepath.Join(workDir, "dev"), 0744)).To(Succeed())
		f, err := os.OpenFile(filepath.Join(workDir, "dev", "iommu"), os.O_RDONLY|os.O_CREATE, 0666)
		Expect(err).ToNot(HaveOccurred())
		f.Close()

		devs := []string{iommuGroup3}
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, err := dpi.Allocate(ctx, &requests)
		Expect(err).ToNot(BeNil())
		Expect(responses).To(BeNil())
	})

	It("Should fail allocation for unknown iommu id", func() {
		devs := []string{iommuGroup4}
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, err := dpi.Allocate(ctx, &requests)
		Expect(err).ToNot(BeNil())
		Expect(responses).To(BeNil())
	})

	It("Should monitor health of device node", func() {
		go dpi.healthCheck()
		Expect(dpi.devs[0].Health).To(Equal(pluginapi.Healthy))

		time.Sleep(1 * time.Second)
		By("Removing a (fake) device node")
		os.Remove(devicePath)

		By("Creating a new (fake) device node")
		fileObj, err := os.Create(devicePath)
		Expect(err).ToNot(HaveOccurred())
		fileObj.Close()
	})

	It("Should list devices and then react to changes in the health of the devices", func() {

		fakeServer := &fakeDevicePluginListAndWatchServer{ServerStream: nil}
		fakeEmpty := &pluginapi.Empty{}
		go dpi.ListAndWatch(fakeEmpty, fakeServer)
		time.Sleep(1 * time.Second)
		Expect(devices[0].ID).To(Equal(iommuGroup1))
		Expect(devices[1].ID).To(Equal(iommuGroup2))
		Expect(devices[0].Health).To(Equal(pluginapi.Healthy))
		Expect(devices[1].Health).To(Equal(pluginapi.Healthy))

		dpi.unhealthy <- iommuGroup2
		time.Sleep(1 * time.Second)
		Expect(devices[0].ID).To(Equal(iommuGroup1))
		Expect(devices[1].ID).To(Equal(iommuGroup2))
		Expect(devices[0].Health).To(Equal(pluginapi.Healthy))
		Expect(devices[1].Health).To(Equal(pluginapi.Unhealthy))

		dpi.healthy <- iommuGroup2
		time.Sleep(1 * time.Second)
		Expect(devices[0].ID).To(Equal(iommuGroup1))
		Expect(devices[1].ID).To(Equal(iommuGroup2))
		Expect(devices[0].Health).To(Equal(pluginapi.Healthy))
		Expect(devices[1].Health).To(Equal(pluginapi.Healthy))
	})
})
