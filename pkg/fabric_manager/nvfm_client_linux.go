//go:build nvfm && cgo

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

// CGO bindings to the NVIDIA Fabric Manager SDK (libnvfm). The SDK is
// provided by the nvidia-fabric-manager-devel package, which installs
// /usr/include/nv_fm_agent.h and libnvfm.so. Only compiled with -tags=nvfm
// so that the default build works on machines without the FM SDK.
//
//	https://docs.nvidia.com/datacenter/tesla/fabric-manager-user-guide/index.html#fabric-manager-sdk

package fabric_manager

/*
#cgo CFLAGS: -I/usr/include
#cgo LDFLAGS: -lnvfm

#include <stdlib.h>
#include "nv_fm_agent.h"
*/
import "C"

import (
	"context"
	"fmt"
	"log"
	"sync"
	"unsafe"
)

// Built reports whether this binary was compiled with libnvfm support.
const Built = true

// nvfmClient talks to a running nv-fabricmanager over the FM SDK. The
// connection is established lazily on first use and cached; on any API
// failure the connection is dropped so the next call reconnects.
type nvfmClient struct {
	cfg Config

	mu        sync.Mutex
	handle    C.fmHandle_t
	connected bool
	libInited bool
}

// NewFMClient returns an FMClient backed by libnvfm.
func NewFMClient(cfg Config) FMClient {
	if cfg.Address == "" {
		cfg.Address = DefaultFMAddress
	}
	if cfg.TimeoutMs == 0 {
		// fmConnect fails when timeoutMs is 0.
		cfg.TimeoutMs = DefaultFMTimeoutMs
	}
	return &nvfmClient{cfg: cfg}
}

// connectLocked initializes the FM library and connects to fabric manager.
// Callers must hold c.mu.
func (c *nvfmClient) connectLocked() error {
	if c.connected {
		return nil
	}

	if !c.libInited {
		if ret := C.fmLibInit(); ret != C.FM_ST_SUCCESS {
			return fmt.Errorf("fmLibInit failed: %d", int32(ret))
		}
		c.libInited = true
	}

	if len(c.cfg.Address) >= int(C.FM_MAX_STR_LENGTH) {
		return fmt.Errorf("fabric manager address %q too long", c.cfg.Address)
	}

	var params C.fmConnectParams_t
	params.version = C.fmConnectParams_version
	params.timeoutMs = C.uint(c.cfg.TimeoutMs)
	if c.cfg.AddressType == AddressTypeUnix {
		params.addressType = uint32(C.NV_FM_API_ADDR_TYPE_UNIX)
	} else {
		params.addressType = uint32(C.NV_FM_API_ADDR_TYPE_INET)
	}
	for i := 0; i < len(c.cfg.Address); i++ {
		params.addressInfo[i] = C.char(c.cfg.Address[i])
	}
	params.addressInfo[len(c.cfg.Address)] = 0

	if ret := C.fmConnect(&params, &c.handle); ret != C.FM_ST_SUCCESS {
		return fmt.Errorf("fmConnect to %s (%s) failed: %d", c.cfg.Address, c.cfg.AddressType, int32(ret))
	}
	c.connected = true
	log.Printf("fabric_manager: connected to fabric manager at %s (%s)", c.cfg.Address, c.cfg.AddressType)
	return nil
}

// disconnectLocked drops the cached connection. Callers must hold c.mu.
func (c *nvfmClient) disconnectLocked() {
	if !c.connected {
		return
	}
	if ret := C.fmDisconnect(c.handle); ret != C.FM_ST_SUCCESS {
		log.Printf("fabric_manager: fmDisconnect failed: %d", int32(ret))
	}
	c.connected = false
}

// GetSupportedPartitions returns all fabric partitions supported by the
// fabric manager instance. The FM SDK is synchronous, so ctx is not used to
// cancel an in-flight call; the connection timeout bounds the call instead.
func (c *nvfmClient) GetSupportedPartitions(ctx context.Context) ([]Partition, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.connectLocked(); err != nil {
		return nil, err
	}

	// fmFabricPartitionList_t is large (max partitions x max GPUs); allocate
	// it on the C heap rather than the Go stack.
	list := (*C.fmFabricPartitionList_t)(C.calloc(1, C.sizeof_fmFabricPartitionList_t))
	if list == nil {
		return nil, fmt.Errorf("failed to allocate fabric partition list")
	}
	defer C.free(unsafe.Pointer(list))
	list.version = C.fmFabricPartitionList_version

	if ret := C.fmGetSupportedFabricPartitions(c.handle, list); ret != C.FM_ST_SUCCESS {
		// Drop the connection so the next call reconnects.
		c.disconnectLocked()
		return nil, fmt.Errorf("fmGetSupportedFabricPartitions failed: %d", int32(ret))
	}

	partitions := make([]Partition, 0, int(list.numPartitions))
	for i := 0; i < int(list.numPartitions); i++ {
		info := &list.partitionInfo[i]
		p := Partition{
			ID:             uint32(info.partitionId),
			IsActive:       info.isActive != 0,
			GPUPhysicalIDs: make([]uint32, 0, int(info.numGpus)),
		}
		for g := 0; g < int(info.numGpus); g++ {
			p.GPUPhysicalIDs = append(p.GPUPhysicalIDs, uint32(info.gpuInfo[g].physicalId))
		}
		partitions = append(partitions, p)
	}
	return partitions, nil
}
