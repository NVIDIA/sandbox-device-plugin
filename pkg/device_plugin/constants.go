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
	"path/filepath"
	"time"

	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	DeviceNamespace = "nvidia.com"
	vfioDevicePath  = "/dev/vfio"
	iommuDevicePath = "/dev/iommu"
	gpuPrefix       = "PCI_RESOURCE_NVIDIA_COM"
	cdiVendor       = "nvidia.com"
)

var (
	// rootPath can be set for testing to simplify testing
	rootPath = "/"
	// cdiRoot can be set for testing to redirect CDI spec output
	cdiRoot = "/var/run/cdi"
	// Flag to allow running GFD pod
	EnableGFD = false

	// healthCheckInterval is how often each plugin re-checks that the device
	// nodes backing its devices still exist. Overridable in tests.
	healthCheckInterval = 10 * time.Second

	// socketCheckInterval is how often the plugin manager verifies that every
	// plugin socket is still present in the kubelet device-plugin directory.
	// This is the backstop for the fsnotify watch on that directory, and the
	// retry timer for plugins whose last re-registration attempt failed.
	// Overridable in tests.
	socketCheckInterval = 30 * time.Second

	// connectionTimeout bounds a single dial of either kubelet's registration
	// socket or this plugin's own socket. Overridable in tests.
	connectionTimeout = 5 * time.Second

	// devicePluginPath is the directory kubelet scans for device plugin
	// sockets, and kubeletSocket is kubelet's registration socket inside it.
	// These are variables rather than the pluginapi constants so that tests can
	// point them at a temporary directory.
	devicePluginPath = pluginapi.DevicePluginPath
	kubeletSocket    = pluginapi.KubeletSocket
)

// setDevicePluginPath redirects the kubelet device-plugin directory. For tests.
func setDevicePluginPath(dir string) {
	devicePluginPath = dir
	kubeletSocket = filepath.Join(dir, filepath.Base(pluginapi.KubeletSocket))
}

func setCdiRoot(path string) {
	cdiRoot = path
}
