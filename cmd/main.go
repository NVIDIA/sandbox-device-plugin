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

package main

import (
	"log"
	"os"
	"strconv"

	"github.com/nvidia/sandbox-device-plugin/pkg/device_plugin"
	"github.com/nvidia/sandbox-device-plugin/pkg/fabric_manager"
)

// isFabricManagerEnabled returns true if fabric manager integration is
// enabled via the ENABLE_FABRIC_MANAGER environment variable.
func isFabricManagerEnabled() bool {
	enabled, _ := strconv.ParseBool(os.Getenv("ENABLE_FABRIC_MANAGER"))
	return enabled
}

// setupFabricManager loads the GPU PCI-to-module mapping and constructs the
// fabric manager client used for partition-aware preferred allocation. On any
// failure it logs and returns without enabling fabric manager support, so the
// device plugin still serves devices normally.
func setupFabricManager() {
	if !fabric_manager.Built {
		log.Printf("ENABLE_FABRIC_MANAGER is set but this binary was built without libnvfm support; partition-aware allocation stays off (rebuild on linux with CGO_ENABLED=1 and -tags=nvfm)")
		return
	}

	mappingPath := fabric_manager.PCIModuleMappingPathFromEnv()
	pciToModule, err := fabric_manager.LoadPCIModuleMapping(mappingPath)
	if err != nil {
		log.Printf("Fabric manager enabled but loading PCI module mapping %q failed: %v; continuing without partition-aware allocation",
			mappingPath, err)
		return
	}

	device_plugin.FMPartition = &device_plugin.FMPartitionConfig{
		Client:      fabric_manager.NewFMClient(fabric_manager.ConfigFromEnv()),
		PCIToModule: pciToModule,
	}
	log.Printf("Fabric manager partition-aware allocation enabled (mapping: %s, %d GPU(s))",
		mappingPath, len(pciToModule))
}

func main() {
	var ok bool
	var enableGFDFlag string
	device_plugin.PGPUAlias, ok = os.LookupEnv("P_GPU_ALIAS")
	if !ok {
		device_plugin.PGPUAlias = "pgpu"
	}
	device_plugin.NVSwitchAlias, ok = os.LookupEnv("NVSWITCH_ALIAS")
	if !ok {
		device_plugin.NVSwitchAlias = "nvswitch"
	}
	enableGFDFlag, ok = os.LookupEnv("ENABLE_GFD")
	// default is false
	if ok {
		device_plugin.EnableGFD, _ = strconv.ParseBool(enableGFDFlag)
	}
	// default is false
	if isFabricManagerEnabled() {
		setupFabricManager()
	}
	device_plugin.InitiateDevicePlugin()
}
