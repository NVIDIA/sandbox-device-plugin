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
	"strings"
	"testing"
)

func TestGenerateCDISpecForClass_NoIOMMU(t *testing.T) {
	tests := []struct {
		name            string
		noiommuParamVal string // empty string means file is absent
		wantPaths       []string
		unwantedPaths   []string
	}{
		{
			name:            "plain group paths when noiommu mode is disabled",
			noiommuParamVal: "N\n",
			wantPaths:       []string{"/dev/vfio/vfio", "/dev/vfio/1", "/dev/vfio/2"},
			unwantedPaths:   []string{"noiommu"},
		},
		{
			name:            "plain group paths when noiommu param is absent",
			noiommuParamVal: "",
			wantPaths:       []string{"/dev/vfio/vfio", "/dev/vfio/1", "/dev/vfio/2"},
			unwantedPaths:   []string{"noiommu"},
		},
		{
			name:            "noiommu- prefixed paths when noiommu mode is enabled",
			noiommuParamVal: "Y\n",
			wantPaths:       []string{"/dev/vfio/vfio", "/dev/vfio/noiommu-1", "/dev/vfio/noiommu-2"},
			unwantedPaths:   []string{"/dev/vfio/1", "/dev/vfio/2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			rootPath = workDir
			setCdiRoot(filepath.Join(workDir, "cdi"))

			iommuMap = map[string][]NvidiaPCIDevice{
				"1": {{Address: "0000:01:00.0", DeviceID: 0x1b80, DeviceName: "GeForce GTX 1080", IommuGroup: 1}},
				"2": {{Address: "0000:02:00.0", DeviceID: 0x1b80, DeviceName: "GeForce GTX 1080", IommuGroup: 2}},
			}

			if tt.noiommuParamVal != "" {
				dir := filepath.Join(workDir, "sys", "module", "vfio", "parameters")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatalf("failed to create sysfs param dir: %v", err)
				}
				if err := os.WriteFile(
					filepath.Join(dir, "enable_unsafe_noiommu_mode"),
					[]byte(tt.noiommuParamVal), 0444,
				); err != nil {
					t.Fatalf("failed to write noiommu param: %v", err)
				}
			}

			if err := generateCDISpecForClass("pgpu", []string{"1", "2"}); err != nil {
				t.Fatalf("generateCDISpecForClass returned error: %v", err)
			}

			files, err := filepath.Glob(filepath.Join(workDir, "cdi", "*.yaml"))
			if err != nil {
				t.Fatalf("failed to glob CDI spec files: %v", err)
			}
			if len(files) != 1 {
				t.Fatalf("expected 1 CDI spec file, got %d", len(files))
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatalf("failed to read CDI spec file: %v", err)
			}
			content := string(data)

			for _, path := range tt.wantPaths {
				if !strings.Contains(content, path) {
					t.Errorf("CDI spec missing expected path %q\nspec content:\n%s", path, content)
				}
			}
			for _, path := range tt.unwantedPaths {
				if strings.Contains(content, path) {
					t.Errorf("CDI spec contains unexpected path %q\nspec content:\n%s", path, content)
				}
			}
		})
	}
}
