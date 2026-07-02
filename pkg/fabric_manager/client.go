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

// Package fabric_manager provides a client for the NVIDIA Fabric Manager (FM)
// Shared NVSwitch partition API and partition-aware preferred-allocation logic
// for the sandbox device plugin. On HGX systems, NVLink between a tenant VM's
// GPUs only works when the assigned GPUs exactly match an FM fabric partition,
// so the device plugin uses this package to steer kubelet allocations towards
// complete partitions.
//
// The design mirrors kubevirt-gpu-device-plugin PR#166 (mresvanis), adapted
// for sandbox-device-plugin. Addresses NVIDIA/sandbox-device-plugin#24.
package fabric_manager

import (
	"context"
	"log"
	"os"
	"strings"
)

const (
	// DefaultFMAddress is the default fabric manager command API address.
	DefaultFMAddress = "127.0.0.1:6666"

	// DefaultFMTimeoutMs is the default connection timeout in milliseconds.
	// fmConnect requires a timeout strictly greater than zero.
	DefaultFMTimeoutMs = 5000

	// AddressTypeInet selects a TCP/IP connection to fabric manager.
	AddressTypeInet = "inet"
	// AddressTypeUnix selects a Unix domain socket connection to fabric manager.
	AddressTypeUnix = "unix"

	fmAddressEnvVar     = "FM_ADDRESS"
	fmAddressTypeEnvVar = "FM_ADDRESS_TYPE"
)

// Partition describes a single fabric partition supported by fabric manager.
type Partition struct {
	// ID is the fabric partition ID.
	ID uint32
	// IsActive reports whether the partition is currently activated.
	IsActive bool
	// GPUPhysicalIDs lists the physical module IDs of the GPUs in the partition.
	GPUPhysicalIDs []uint32
}

// FMClient is the interface to the fabric manager partition API.
type FMClient interface {
	// GetSupportedPartitions returns all fabric partitions supported by the
	// fabric manager instance.
	GetSupportedPartitions(ctx context.Context) ([]Partition, error)
}

// Config contains connection options for the fabric manager client.
type Config struct {
	// Address is the fabric manager command API address, e.g. "127.0.0.1:6666"
	// for inet or a socket path for unix.
	Address string
	// AddressType is either AddressTypeInet or AddressTypeUnix.
	AddressType string
	// TimeoutMs is the connection timeout in milliseconds; must be > 0.
	TimeoutMs uint32
}

// ConfigFromEnv builds a Config from the FM_ADDRESS and FM_ADDRESS_TYPE
// environment variables, falling back to defaults when unset or invalid.
func ConfigFromEnv() Config {
	cfg := Config{
		Address:     DefaultFMAddress,
		AddressType: AddressTypeInet,
		TimeoutMs:   DefaultFMTimeoutMs,
	}
	if v := os.Getenv(fmAddressEnvVar); v != "" {
		cfg.Address = v
	}
	if v := os.Getenv(fmAddressTypeEnvVar); v != "" {
		switch strings.ToLower(v) {
		case AddressTypeInet, AddressTypeUnix:
			cfg.AddressType = strings.ToLower(v)
		default:
			log.Printf("fabric_manager: unknown %s value %q, using %q", fmAddressTypeEnvVar, v, cfg.AddressType)
		}
	}
	return cfg
}
