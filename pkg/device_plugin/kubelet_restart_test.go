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
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// fakeKubelet is a minimal implementation of the kubelet side of the device
// plugin registration API. It records every Register call it receives.
type fakeKubelet struct {
	pluginapi.UnimplementedRegistrationServer

	mu       sync.Mutex
	server   *grpc.Server
	requests []*pluginapi.RegisterRequest
}

func (f *fakeKubelet) Register(ctx context.Context, r *pluginapi.RegisterRequest) (*pluginapi.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	return &pluginapi.Empty{}, nil
}

// registrations returns the endpoint/resource pairs registered so far.
func (f *fakeKubelet) registrations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.requests))
	for _, r := range f.requests {
		out = append(out, r.ResourceName+"@"+r.Endpoint)
	}
	return out
}

// startFakeKubelet creates kubelet.sock in the device plugin directory and
// serves the registration API on it, the way a freshly initialized kubelet does.
func startFakeKubelet() *fakeKubelet {
	Expect(os.RemoveAll(kubeletSocket)).To(Succeed())

	lis, err := net.Listen("unix", kubeletSocket)
	Expect(err).ToNot(HaveOccurred())

	f := &fakeKubelet{server: grpc.NewServer()}
	pluginapi.RegisterRegistrationServer(f.server, f)
	go f.server.Serve(lis)

	return f
}

func (f *fakeKubelet) stop() {
	f.server.Stop()
}

var _ = Describe("Re-registration when kubelet restarts", func() {
	var (
		pluginDir           string
		devDir              string
		dp                  *GenericDevicePlugin
		managerStop         chan struct{}
		managerDone         chan struct{}
		origDevicePluginDir string
		origKubeletSocket   string
		origSocketInterval  time.Duration
		origHealthInterval  time.Duration
		origConnTimeout     time.Duration
		origStop            chan struct{}
		origRootPath        string
	)

	BeforeEach(func() {
		var err error
		pluginDir, err = os.MkdirTemp("", "dp-sock")
		Expect(err).ToNot(HaveOccurred())
		devDir, err = os.MkdirTemp("", "dp-dev")
		Expect(err).ToNot(HaveOccurred())

		origDevicePluginDir, origKubeletSocket = devicePluginPath, kubeletSocket
		origSocketInterval, origHealthInterval = socketCheckInterval, healthCheckInterval
		origConnTimeout = connectionTimeout
		origStop, origRootPath = stop, rootPath

		setDevicePluginPath(pluginDir)
		// Keep the test fast; the production defaults are 30s / 10s.
		socketCheckInterval = 100 * time.Millisecond
		healthCheckInterval = 100 * time.Millisecond
		// Keep a failed dial from blocking the manager for the production 5s.
		connectionTimeout = 500 * time.Millisecond
		// No /dev/iommu under rootPath, so the legacy vfio device naming is used.
		rootPath = devDir

		// One "GPU" whose device node exists.
		Expect(os.WriteFile(filepath.Join(devDir, "1"), nil, 0o600)).To(Succeed())
		dp = NewGenericDevicePlugin("gpu", devDir+"/", []*pluginapi.Device{
			{ID: "1", Health: pluginapi.Healthy},
		})

		managerStop = make(chan struct{})
		stop = managerStop
		managerDone = make(chan struct{})
	})

	AfterEach(func() {
		close(managerStop)
		Eventually(managerDone, "15s").Should(BeClosed())

		devicePluginPath, kubeletSocket = origDevicePluginDir, origKubeletSocket
		socketCheckInterval, healthCheckInterval = origSocketInterval, origHealthInterval
		connectionTimeout = origConnTimeout
		stop, rootPath = origStop, origRootPath
		os.RemoveAll(pluginDir)
		os.RemoveAll(devDir)
	})

	startManager := func() {
		go func() {
			defer close(managerDone)
			runPluginManager([]*GenericDevicePlugin{dp})
		}()
	}

	It("re-registers after kubelet wipes the device-plugin directory", func() {
		By("registering against the kubelet that is on its way out")
		outgoing := startFakeKubelet()
		Expect(dp.Start(managerStop)).To(Succeed())
		Expect(outgoing.registrations()).To(ConsistOf("nvidia.com/gpu@sandbox-gpu.sock"))
		Expect(dp.socketPath).To(BeAnExistingFile())

		startManager()

		By("having kubelet initialize: it wipes the directory and recreates kubelet.sock")
		outgoing.stop()
		entries, err := os.ReadDir(pluginDir)
		Expect(err).ToNot(HaveOccurred())
		for _, e := range entries {
			Expect(os.Remove(filepath.Join(pluginDir, e.Name()))).To(Succeed())
		}
		// The plugin is now in the broken state from the incident: process
		// alive, gRPC server serving on an unlinked inode, socket gone.
		Expect(dp.socketPath).ToNot(BeAnExistingFile())

		incoming := startFakeKubelet()
		defer incoming.stop()

		By("expecting the plugin to notice and register with the new kubelet")
		Eventually(incoming.registrations, "10s", "100ms").
			Should(ContainElement("nvidia.com/gpu@sandbox-gpu.sock"))
		Eventually(func() bool {
			return dp.registered()
		}, "10s", "100ms").Should(BeTrue())
		Expect(dp.socketPath).To(BeAnExistingFile())
	})

	It("re-registers when only its own socket is removed", func() {
		kubelet := startFakeKubelet()
		defer kubelet.stop()

		Expect(dp.Start(managerStop)).To(Succeed())
		Expect(kubelet.registrations()).To(HaveLen(1))

		startManager()

		By("deleting just the plugin socket, leaving kubelet.sock in place")
		Expect(os.Remove(dp.socketPath)).To(Succeed())

		By("expecting the periodic socket check to restore the registration")
		Eventually(func() int { return len(kubelet.registrations()) }, "10s", "100ms").
			Should(BeNumerically(">=", 2))
		Expect(dp.socketPath).To(BeAnExistingFile())
	})

	It("retries a plugin whose initial registration failed", func() {
		By("starting the plugin with no kubelet listening at all")
		Expect(dp.Start(managerStop)).ToNot(Succeed())

		startManager()

		By("bringing kubelet up afterwards")
		kubelet := startFakeKubelet()
		defer kubelet.stop()

		Eventually(kubelet.registrations, "10s", "100ms").
			Should(ContainElement("nvidia.com/gpu@sandbox-gpu.sock"))
	})

	It("does not restart plugins while everything is healthy", func() {
		kubelet := startFakeKubelet()
		defer kubelet.stop()

		Expect(dp.Start(managerStop)).To(Succeed())
		startManager()

		// Several socket-check intervals must pass without a spurious restart:
		// cleanup() removing our own socket must not feed back into the watcher.
		Consistently(kubelet.registrations, "2s", "200ms").Should(HaveLen(1))
	})
})

var _ = Describe("Device health checks", func() {
	var (
		devDir             string
		dp                 *GenericDevicePlugin
		hcStop             chan struct{}
		origHealthInterval time.Duration
		origRootPath       string
	)

	BeforeEach(func() {
		var err error
		devDir, err = os.MkdirTemp("", "dp-health")
		Expect(err).ToNot(HaveOccurred())

		origHealthInterval, origRootPath = healthCheckInterval, rootPath
		healthCheckInterval = 50 * time.Millisecond
		rootPath = devDir

		Expect(os.WriteFile(filepath.Join(devDir, "1"), nil, 0o600)).To(Succeed())
		dp = NewGenericDevicePlugin("gpu", devDir+"/", []*pluginapi.Device{
			{ID: "1", Health: pluginapi.Healthy},
		})
		hcStop = make(chan struct{})
		dp.stop = hcStop
	})

	AfterEach(func() {
		close(hcStop)
		healthCheckInterval, rootPath = origHealthInterval, origRootPath
		os.RemoveAll(devDir)
	})

	It("reports a device unhealthy when its node disappears and healthy when it returns", func() {
		go dp.healthCheck()

		Expect(os.Remove(filepath.Join(devDir, "1"))).To(Succeed())
		Eventually(dp.unhealthy, "5s").Should(Receive(Equal("1")))

		Expect(os.WriteFile(filepath.Join(devDir, "1"), nil, 0o600)).To(Succeed())
		Eventually(dp.healthy, "5s").Should(Receive(Equal("1")))
	})

	It("detects changes from the directory watch without waiting for the poll", func() {
		go dp.runHealthCheck(make(chan struct{}), time.Hour)

		// Give the watch time to be set up before the first change.
		time.Sleep(200 * time.Millisecond)

		Expect(os.Remove(filepath.Join(devDir, "1"))).To(Succeed())
		Eventually(dp.unhealthy, "2s").Should(Receive(Equal("1")))

		Expect(os.WriteFile(filepath.Join(devDir, "1"), nil, 0o600)).To(Succeed())
		Eventually(dp.healthy, "2s").Should(Receive(Equal("1")))
	})

	It("re-establishes the watch after the device directory is recreated", func() {
		go dp.runHealthCheck(make(chan struct{}), 3*time.Second)
		time.Sleep(200 * time.Millisecond)

		Expect(os.RemoveAll(devDir)).To(Succeed())
		Eventually(dp.unhealthy, "2s").Should(Receive(Equal("1")))

		// With the directory gone there is nothing to watch, so the poll has
		// to notice the device returning. That pass also re-arms the watch
		// before it reports the device healthy.
		Expect(os.Mkdir(devDir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(devDir, "1"), nil, 0o600)).To(Succeed())
		Eventually(dp.healthy, "5s").Should(Receive(Equal("1")))

		// Well inside the next poll, so only the re-armed watch can see this.
		Expect(os.Remove(filepath.Join(devDir, "1"))).To(Succeed())
		Eventually(dp.unhealthy, "1s").Should(Receive(Equal("1")))
	})

	It("stops when the gRPC server generation is torn down", func() {
		dp.mu.Lock()
		dp.done = make(chan struct{})
		done := dp.done
		dp.mu.Unlock()

		exited := make(chan struct{})
		go func() {
			defer close(exited)
			dp.healthCheck()
		}()

		close(done)
		Eventually(exited, "5s").Should(BeClosed())
	})
})
