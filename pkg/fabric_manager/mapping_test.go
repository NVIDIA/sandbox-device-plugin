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
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	fm "github.com/nvidia/sandbox-device-plugin/pkg/fabric_manager"
)

var _ = Describe("PCI module mapping", func() {
	writeMapping := func(content string) string {
		path := filepath.Join(GinkgoT().TempDir(), "mapping.json")
		Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
		return path
	}

	Context("LoadPCIModuleMapping()", func() {
		It("parses numeric module IDs", func() {
			path := writeMapping(`{"0000:19:00.0": 1, "0000:3b:00.0": 2}`)
			mapping, err := fm.LoadPCIModuleMapping(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(mapping).To(Equal(map[string]uint32{
				"0000:19:00.0": 1,
				"0000:3b:00.0": 2,
			}))
		})

		It("parses string module IDs (kubevirt-gpu-device-plugin PR#166 format)", func() {
			path := writeMapping(`{"0000:19:00.0": "1", "0000:3b:00.0": "2"}`)
			mapping, err := fm.LoadPCIModuleMapping(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(mapping).To(Equal(map[string]uint32{
				"0000:19:00.0": 1,
				"0000:3b:00.0": 2,
			}))
		})

		It("normalizes 8-digit-domain uppercase BDFs to sysfs format", func() {
			path := writeMapping(`{"00000000:AB:00.0": "8", "00000000:2A:00.0": 4}`)
			mapping, err := fm.LoadPCIModuleMapping(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(mapping).To(Equal(map[string]uint32{
				"0000:ab:00.0": 8,
				"0000:2a:00.0": 4,
			}))
		})

		It("returns an error for a missing file", func() {
			_, err := fm.LoadPCIModuleMapping(filepath.Join(GinkgoT().TempDir(), "missing.json"))
			Expect(err).To(MatchError(ContainSubstring("failed to read PCI module mapping file")))
		})

		It("returns an error for invalid JSON", func() {
			path := writeMapping(`not json`)
			_, err := fm.LoadPCIModuleMapping(path)
			Expect(err).To(MatchError(ContainSubstring("failed to parse PCI module mapping JSON")))
		})

		DescribeTable("returns an error for invalid module ID values",
			func(content string) {
				path := writeMapping(content)
				_, err := fm.LoadPCIModuleMapping(path)
				Expect(err).To(MatchError(ContainSubstring("invalid module ID")))
			},
			Entry("non-numeric string", `{"0000:19:00.0": "not-a-number"}`),
			Entry("negative number", `{"0000:19:00.0": -1}`),
			Entry("fractional number", `{"0000:19:00.0": 1.5}`),
			Entry("boolean value", `{"0000:19:00.0": true}`),
		)
	})

	Context("NormalizePCIAddress()", func() {
		DescribeTable("normalizes to 4-digit lowercase hex domain",
			func(input, expected string) {
				Expect(fm.NormalizePCIAddress(input)).To(Equal(expected))
			},
			Entry("already normalized", "0000:19:00.0", "0000:19:00.0"),
			Entry("uppercase hex", "0000:AB:00.0", "0000:ab:00.0"),
			Entry("8-digit domain", "00000000:ab:00.0", "0000:ab:00.0"),
			Entry("no domain separator", "invalid", "invalid"),
			Entry("non-hex domain", "zzzz:ab:00.0", "zzzz:ab:00.0"),
		)
	})
})
