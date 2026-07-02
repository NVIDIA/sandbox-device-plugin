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
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	// DefaultPCIModuleMappingPath is the default location of the GPU
	// PCI-to-module mapping JSON file produced on the host.
	DefaultPCIModuleMappingPath = "/run/nvidia-fabricmanager/gpu-pci-module-mapping.json"

	mappingPathEnvVar = "GPU_PCI_MODULE_MAPPING_PATH"
)

// PCIModuleMappingPathFromEnv returns the mapping file path from the
// GPU_PCI_MODULE_MAPPING_PATH environment variable, or the default path.
func PCIModuleMappingPathFromEnv() string {
	if v := os.Getenv(mappingPathEnvVar); v != "" {
		return v
	}
	return DefaultPCIModuleMappingPath
}

// NormalizePCIAddress normalizes a PCI BDF address to the sysfs format used
// by the Linux kernel (4-digit lowercase hex domain). Mapping files produced
// by NVIDIA tooling may use an 8-digit domain and uppercase hex (e.g.
// "00000000:AB:00.0"), while sysfs uses "0000:ab:00.0".
func NormalizePCIAddress(addr string) string {
	lower := strings.ToLower(strings.TrimSpace(addr))

	colonIdx := strings.Index(lower, ":")
	if colonIdx < 0 {
		return lower
	}

	domain := lower[:colonIdx]
	rest := lower[colonIdx:]

	domainVal, err := strconv.ParseUint(domain, 16, 32)
	if err != nil {
		return lower
	}

	if domainVal <= 0xFFFF {
		return fmt.Sprintf("%04x%s", domainVal, rest)
	}
	return fmt.Sprintf("%08x%s", domainVal, rest)
}

// LoadPCIModuleMapping reads a JSON file mapping GPU PCI BDF addresses to
// physical module IDs and returns the mapping with normalized PCI addresses.
// Both value forms are accepted for compatibility:
//
//	{"0000:19:00.0": 1}   (numeric module IDs)
//	{"0000:19:00.0": "1"} (string module IDs, as used by kubevirt-gpu-device-plugin PR#166)
func LoadPCIModuleMapping(path string) (map[string]uint32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read PCI module mapping file: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var raw map[string]interface{}
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to parse PCI module mapping JSON: %w", err)
	}

	pciToModule := make(map[string]uint32, len(raw))
	for pciAddr, value := range raw {
		var moduleIDStr string
		switch v := value.(type) {
		case string:
			moduleIDStr = v
		case json.Number:
			moduleIDStr = v.String()
		default:
			return nil, fmt.Errorf("invalid module ID %v for PCI address %s: expected number or string", value, pciAddr)
		}
		moduleID, err := strconv.ParseUint(moduleIDStr, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid module ID %q for PCI address %s: %w", moduleIDStr, pciAddr, err)
		}
		pciToModule[NormalizePCIAddress(pciAddr)] = uint32(moduleID)
	}

	return pciToModule, nil
}
