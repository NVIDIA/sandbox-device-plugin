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
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
)

var _ = Describe("CDI", func() {
	Context("GenerateCDISpec() Tests", func() {
		var workDir string
		var savedRootPath, savedCdiRoot, savedPGPUAlias string
		var savedIommuMap map[string][]NvidiaPCIDevice
		var savedDeviceMap map[string][]string
		var savedNVSwitchDeviceIDs map[string]bool

		BeforeEach(func() {
			savedRootPath, savedCdiRoot, savedPGPUAlias = rootPath, cdiRoot, PGPUAlias
			savedIommuMap, savedDeviceMap, savedNVSwitchDeviceIDs = iommuMap, deviceMap, nvSwitchDeviceIDs

			var err error
			workDir, err = os.MkdirTemp("", "cdi-test")
			Expect(err).ToNot(HaveOccurred())
			// No <rootPath>/dev/iommu, so legacy VFIO groups are used.
			rootPath = workDir
			setCdiRoot(filepath.Join(workDir, "cdi"))
			PGPUAlias = "pgpu"
			nvSwitchDeviceIDs = map[string]bool{}
		})

		AfterEach(func() {
			rootPath, cdiRoot, PGPUAlias = savedRootPath, savedCdiRoot, savedPGPUAlias
			iommuMap, deviceMap, nvSwitchDeviceIDs = savedIommuMap, savedDeviceMap, savedNVSwitchDeviceIDs
			os.RemoveAll(workDir)
		})

		readPGPUSpec := func() *cdiapi.Spec {
			spec, err := cdiapi.ReadSpec(filepath.Join(workDir, "cdi", "nvidia.com-pgpu.yaml"), 0)
			Expect(err).ToNot(HaveOccurred())
			return spec
		}

		devicePaths := func(spec *cdiapi.Spec, name string) []string {
			dev := spec.GetDevice(name)
			Expect(dev).ToNot(BeNil())
			var paths []string
			for _, node := range dev.ContainerEdits.DeviceNodes {
				paths = append(paths, node.Path)
			}
			return paths
		}

		It("generates one CDI device per IOMMU group", func() {
			iommuMap = map[string][]NvidiaPCIDevice{
				"8": {{Address: "0000:3b:00.0", DeviceID: 0x2330, DeviceName: "GH100 [H100 SXM5 80GB]", IommuGroup: 8}},
				"9": {{Address: "0000:5e:00.0", DeviceID: 0x2330, DeviceName: "GH100 [H100 SXM5 80GB]", IommuGroup: 9}},
			}
			deviceMap = map[string][]string{"2330": {"8", "9"}}

			Expect(GenerateCDISpec()).To(Succeed())
			spec := readPGPUSpec()
			Expect(spec.Devices).To(HaveLen(2))
			Expect(devicePaths(spec, "8")).To(Equal([]string{"/dev/vfio/vfio", "/dev/vfio/8"}))
			Expect(devicePaths(spec, "9")).To(Equal([]string{"/dev/vfio/vfio", "/dev/vfio/9"}))
		})

		It("generates a single CDI device for GPUs sharing an IOMMU group", func() {
			iommuMap = map[string][]NvidiaPCIDevice{
				"8": {
					{Address: "0000:3b:00.0", DeviceID: 0x2330, DeviceName: "GH100 [H100 SXM5 80GB]", IommuGroup: 8},
					{Address: "0000:3c:00.0", DeviceID: 0x2330, DeviceName: "GH100 [H100 SXM5 80GB]", IommuGroup: 8},
				},
				"9": {{Address: "0000:5e:00.0", DeviceID: 0x2330, DeviceName: "GH100 [H100 SXM5 80GB]", IommuGroup: 9}},
			}
			deviceMap = map[string][]string{"2330": {"8", "9"}}

			Expect(GenerateCDISpec()).To(Succeed())
			spec := readPGPUSpec()
			Expect(spec.Devices).To(HaveLen(2))
			Expect(devicePaths(spec, "8")).To(Equal([]string{"/dev/vfio/vfio", "/dev/vfio/8"}))
			Expect(devicePaths(spec, "9")).To(Equal([]string{"/dev/vfio/vfio", "/dev/vfio/9"}))
		})
	})
})
