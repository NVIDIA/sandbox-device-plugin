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
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

var returnIommuMap = getIommuMap

// Implements the kubernetes device plugin API
type GenericDevicePlugin struct {
	devs       []*pluginapi.Device
	socketPath string
	stop       chan struct{} // this channel signals a process-wide shutdown
	healthy    chan string
	unhealthy  chan string
	devicePath string
	deviceName string
	devsHealth []*pluginapi.Device

	// mu guards server and done, which are replaced every time the plugin
	// re-registers with a restarted kubelet.
	mu     sync.Mutex
	server *grpc.Server
	// registeredWithKubelet records whether the last Register call for the
	// current server generation succeeded.
	registeredWithKubelet bool
	// done is closed when the current gRPC server generation is torn down.
	// ListAndWatch and healthCheck use it so that goroutines belonging to a
	// previous generation cannot outlive it.
	done chan struct{}
}

// Returns an initialized instance of GenericDevicePlugin
func NewGenericDevicePlugin(deviceName string, devicePath string, devices []*pluginapi.Device) *GenericDevicePlugin {
	log.Println("Devicename " + deviceName)
	serverSock := filepath.Join(devicePluginPath, fmt.Sprintf("sandbox-%s.sock", deviceName))
	dpi := &GenericDevicePlugin{
		devs:       devices,
		socketPath: serverSock,
		healthy:    make(chan string),
		unhealthy:  make(chan string),
		deviceName: deviceName,
		devicePath: devicePath,
	}
	return dpi
}

func waitForGrpcServer(socketPath string, timeout time.Duration) error {
	conn, err := connect(socketPath, timeout)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// dial establishes the gRPC communication with the registered device plugin.
func connect(socketPath string, timeout time.Duration) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := grpc.DialContext(ctx, socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			if deadline, ok := ctx.Deadline(); ok {
				return net.DialTimeout("unix", addr, time.Until(deadline))
			}
			return net.DialTimeout("unix", addr, connectionTimeout)
		}),
	)
	if err != nil {
		return nil, err
	}

	return c, nil
}

// Start starts the gRPC server of the device plugin and registers it with kubelet.
func (dpi *GenericDevicePlugin) Start(stop chan struct{}) error {
	dpi.mu.Lock()
	defer dpi.mu.Unlock()

	if dpi.stop == nil {
		dpi.stop = stop
	}

	return dpi.startUnprotected()
}

// startUnprotected brings up a new gRPC server generation. dpi.mu must be held.
func (dpi *GenericDevicePlugin) startUnprotected() error {
	if dpi.server != nil {
		return fmt.Errorf("gRPC server already started")
	}

	err := dpi.cleanup()
	if err != nil {
		return err
	}

	sock, err := net.Listen("unix", dpi.socketPath)
	if err != nil {
		log.Printf("[%s] Error creating GRPC server socket: %v", dpi.deviceName, err)
		return err
	}

	dpi.done = make(chan struct{})
	dpi.registeredWithKubelet = false
	dpi.server = grpc.NewServer([]grpc.ServerOption{}...)
	pluginapi.RegisterDevicePluginServer(dpi.server, dpi)

	go dpi.server.Serve(sock)

	if err := waitForGrpcServer(dpi.socketPath, connectionTimeout); err != nil {
		// Not fatal on its own: Register below is the authoritative check.
		log.Printf("[%s] Error connecting to GRPC server: %v", dpi.deviceName, err)
	}

	if err := dpi.Register(); err != nil {
		log.Printf("[%s] Error registering with device plugin manager: %v", dpi.deviceName, err)
		return err
	}
	dpi.registeredWithKubelet = true

	// Hand the generation's teardown channel and interval over while dpi.mu is
	// held, rather than having the goroutine look them up once it is running.
	go dpi.runHealthCheck(dpi.done, healthCheckInterval)

	log.Println(dpi.deviceName + " Device plugin server ready")

	return nil
}

// Stop stops the gRPC server. It is safe to call on a plugin that was never
// started, and safe to call more than once.
func (dpi *GenericDevicePlugin) Stop() error {
	dpi.mu.Lock()
	defer dpi.mu.Unlock()

	return dpi.stopUnprotected()
}

// stopUnprotected tears down the current gRPC server generation. dpi.mu must be held.
func (dpi *GenericDevicePlugin) stopUnprotected() error {
	dpi.registeredWithKubelet = false

	// Signal ListAndWatch and healthCheck for this generation to return.
	if dpi.done != nil {
		close(dpi.done)
		dpi.done = nil
	}

	if dpi.server == nil {
		return nil
	}

	dpi.server.Stop()
	dpi.server = nil

	return dpi.cleanup()
}

// restart tears down the plugin's gRPC server and registers it with kubelet
// again. This is how the plugin recovers when kubelet restarts and wipes
// /var/lib/kubelet/device-plugins out from under a plugin that had already
// registered successfully against the previous kubelet.
func (dpi *GenericDevicePlugin) restart() error {
	log.Printf("Restarting %s device plugin server", dpi.deviceName)

	dpi.mu.Lock()
	defer dpi.mu.Unlock()

	if err := dpi.stopUnprotected(); err != nil {
		// Losing the old socket file is expected here: kubelet just deleted it.
		log.Printf("[%s] Error stopping device plugin server: %v", dpi.deviceName, err)
	}

	return dpi.startUnprotected()
}

// registered reports whether kubelet can currently reach this plugin: it
// registered successfully and its socket is still present on disk.
//
// The socket check is the one that matters for the cold-boot race. When
// kubelet wipes /var/lib/kubelet/device-plugins the process stays alive and
// keeps serving gRPC on an unlinked inode, so every in-process signal still
// looks healthy while kubelet advertises zero devices.
func (dpi *GenericDevicePlugin) registered() bool {
	dpi.mu.Lock()
	ok := dpi.server != nil && dpi.registeredWithKubelet
	dpi.mu.Unlock()

	if !ok {
		return false
	}

	_, err := os.Stat(dpi.socketPath)
	return err == nil
}

// doneChan returns the teardown channel for the current server generation.
func (dpi *GenericDevicePlugin) doneChan() chan struct{} {
	dpi.mu.Lock()
	defer dpi.mu.Unlock()

	return dpi.done
}

// Register registers the device plugin for the given resourceName with Kubelet.
func (dpi *GenericDevicePlugin) Register() error {
	conn, err := connect(kubeletSocket, connectionTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	reqt := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     path.Base(dpi.socketPath),
		ResourceName: fmt.Sprintf("%s/%s", DeviceNamespace, dpi.deviceName),
	}

	_, err = client.Register(context.Background(), reqt)
	if err != nil {
		return err
	}
	return nil
}

// ListAndWatch lists devices and update that list according to the health status
func (dpi *GenericDevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	// Pin the teardown channel for the generation this stream belongs to, so a
	// subsequent re-registration cannot leave this stream running.
	done := dpi.doneChan()

	s.Send(&pluginapi.ListAndWatchResponse{Devices: dpi.devs})

	for {
		select {
		case unhealthy := <-dpi.unhealthy:
			log.Printf("In watch unhealthy")
			for _, dev := range dpi.devs {
				if unhealthy == dev.ID {
					dev.Health = pluginapi.Unhealthy
				}
			}
			s.Send(&pluginapi.ListAndWatchResponse{Devices: dpi.devs})
		case healthy := <-dpi.healthy:
			log.Printf("In watch healthy")
			for _, dev := range dpi.devs {
				if healthy == dev.ID {
					dev.Health = pluginapi.Healthy
				}
			}
			s.Send(&pluginapi.ListAndWatchResponse{Devices: dpi.devs})
		case <-dpi.stop:
			return nil
		case <-done:
			return nil
		}
	}
}

// Allocate performs allocation of devices based on the request
func (dpi *GenericDevicePlugin) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	responses := pluginapi.AllocateResponse{}
	iommufdSupported, err := supportsIOMMUFD()
	if err != nil {
		return nil, fmt.Errorf("could not determine iommufd support: %w", err)
	}
	for _, req := range reqs.ContainerRequests {
		deviceSpecs := make([]*pluginapi.DeviceSpec, 0)
		for _, iommuID := range req.DevicesIDs {
			returnedMap := returnIommuMap()
			// Retrieve the devices associated with the IOMMU group/fd
			nvDevs, ok := returnedMap[iommuID]
			if !ok {
				return nil, fmt.Errorf("invalid allocation request: unknown iommu id: %s", iommuID)
			}

			if iommufdSupported {
				for _, dev := range nvDevs {
					log.Printf("iommufd: allocating device %s (iommufd: %s)", dev.Address, dev.IommuFD)
					if dev.IommuFD == "" {
						return nil, fmt.Errorf("iommufd device not available for device %s", dev.Address)
					}
					deviceSpecs = append(deviceSpecs, &pluginapi.DeviceSpec{
						HostPath:      filepath.Join(vfioDevicePath, "devices", dev.IommuFD),
						ContainerPath: filepath.Join(vfioDevicePath, "devices", dev.IommuFD),
						Permissions:   "mrw",
					})
				}
			} else {
				for _, dev := range nvDevs {
					log.Printf("vfio: allocating device %s (IOMMU group: %d)", dev.Address, dev.IommuGroup)
				}
				deviceSpecs = append(deviceSpecs, &pluginapi.DeviceSpec{
					HostPath:      filepath.Join(vfioDevicePath, "vfio"),
					ContainerPath: filepath.Join(vfioDevicePath, "vfio"),
					Permissions:   "mrw",
				})
				deviceSpecs = append(deviceSpecs, &pluginapi.DeviceSpec{
					HostPath:      filepath.Join(vfioDevicePath, iommuID),
					ContainerPath: filepath.Join(vfioDevicePath, iommuID),
					Permissions:   "mrw",
				})
			}
		}
		response := pluginapi.ContainerAllocateResponse{
			Devices: deviceSpecs,
		}
		log.Printf("Allocated devices %v", response)

		responses.ContainerResponses = append(responses.ContainerResponses, &response)
	}

	return &responses, nil
}

func (dpi *GenericDevicePlugin) cleanup() error {
	if err := os.Remove(dpi.socketPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (dpi *GenericDevicePlugin) GetDevicePluginOptions(ctx context.Context, e *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	options := &pluginapi.DevicePluginOptions{
		PreStartRequired: false,
	}
	return options, nil
}

func (dpi *GenericDevicePlugin) PreStartContainer(ctx context.Context, in *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	res := &pluginapi.PreStartContainerResponse{}
	return res, nil
}

// GetPreferredAllocation is for compatible with new DevicePluginServer API for DevicePlugin service. It has not been implemented in kubevrit-gpu-device-plugin
func (dpi *GenericDevicePlugin) GetPreferredAllocation(ctx context.Context, in *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	// TODO
	// returns a preferred set of devices to allocate
	// from a list of available ones. The resulting preferred allocation is not
	// guaranteed to be the allocation ultimately performed by the
	// devicemanager. It is only designed to help the devicemanager make a more
	// informed allocation decision when possible.
	return nil, nil
}

// healthCheck monitors the device nodes backing this plugin's devices and
// reports health transitions to ListAndWatch.
//
// Device nodes are checked with os.Stat(), and the check runs whenever the
// device directory (/dev/vfio or /dev/vfio/devices) changes, plus every
// interval as a backstop for missed events or a watch that could not be set up.
//
// The watch is on the directory, never on the device nodes themselves. The
// nodes are character devices (crw------- root:root), and inotify_add_watch()
// needs read permission on the inode, which routes through the device cgroup
// check. This DaemonSet mounts /dev/vfio as a hostPath but is not granted those
// devices by the container runtime, so the kernel answers EPERM. The directory
// is an ordinary directory, and os.Stat() only needs search permission on the
// parent directories, so neither is subject to that check.
//
// A watch on a device node would also be unable to ever report a device
// healthy again: an inotify watch on a non-directory dies with the inode, so
// no Create event can be delivered for the path once it is gone.
//
// This function never returns an error that matters to anything else. It runs
// in its own goroutine and its failure must not be able to take down
// re-registration, which is handled by runPluginManager.
func (dpi *GenericDevicePlugin) healthCheck() error {
	return dpi.runHealthCheck(dpi.doneChan(), healthCheckInterval)
}

// runHealthCheck is healthCheck for the server generation torn down by done.
func (dpi *GenericDevicePlugin) runHealthCheck(done chan struct{}, interval time.Duration) error {
	method := fmt.Sprintf("healthCheck(%s)", dpi.deviceName)

	iommufdSupported, err := supportsIOMMUFD()
	if err != nil {
		log.Printf("%s: could not determine iommufd support, assuming legacy vfio: %v", method, err)
	}

	// Device ID -> device node that must exist for that device to be healthy.
	devicePaths := make(map[string]string, len(dpi.devs))
	lastHealthy := make(map[string]bool, len(dpi.devs))
	for _, dev := range dpi.devs {
		devID := dev.ID
		if iommufdSupported {
			devID = fmt.Sprintf("vfio%s", dev.ID)
		}
		devicePaths[dev.ID] = filepath.Join(dpi.devicePath, devID)
		lastHealthy[dev.ID] = dev.Health == pluginapi.Healthy
	}

	log.Printf("%s: checking %d device node(s) under %s on change and every %s",
		method, len(devicePaths), dpi.devicePath, interval)

	watchDir := filepath.Clean(dpi.devicePath)
	var watcher *fsnotify.Watcher
	watchFailing := false
	defer func() {
		if watcher != nil {
			watcher.Close()
		}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		// (Re-)establish the directory watch before checking, so that a change
		// between the check and the watch being set up is not missed.
		if watcher == nil {
			watcher, err = newDirWatcher(watchDir)
			if err != nil {
				// Retried every interval; only log the first failure.
				if !watchFailing {
					log.Printf("%s: unable to watch %s, relying on the %s poll: %v", method, watchDir, interval, err)
				}
				watcher = nil
				watchFailing = true
			} else if watchFailing {
				log.Printf("%s: watching %s again", method, watchDir)
				watchFailing = false
			}
		}

		for id, devPath := range devicePaths {
			healthy := deviceNodePresent(method, devPath)
			if healthy == lastHealthy[id] {
				continue
			}

			ch := dpi.unhealthy
			if healthy {
				log.Printf("%s: device node %s reappeared, marking device %s healthy", method, devPath, id)
				ch = dpi.healthy
			} else {
				log.Printf("%s: device node %s disappeared, marking device %s unhealthy", method, devPath, id)
			}

			// Only record the transition once ListAndWatch has taken it.
			// Kubelet may not have opened the stream yet; blocking here until
			// it does is correct, and both channels below cancel it.
			select {
			case ch <- id:
				lastHealthy[id] = healthy
			case <-dpi.stop:
				return nil
			case <-done:
				return nil
			}
		}

		var events chan fsnotify.Event
		var watchErrors chan error
		if watcher != nil {
			events, watchErrors = watcher.Events, watcher.Errors
		}

		select {
		case <-ticker.C:
		case event, ok := <-events:
			// Any change in the directory triggers a full check; the event
			// itself does not need to name one of our devices. If the
			// directory itself went away, the watch is gone with it, so drop
			// it and let the next pass set it up again.
			if !ok || (event.Name == watchDir && event.Has(fsnotify.Remove|fsnotify.Rename)) {
				log.Printf("%s: lost watch on %s, re-establishing", method, watchDir)
				watcher.Close()
				watcher = nil
			}
		case err, ok := <-watchErrors:
			// Typically an event queue overflow. The check that follows
			// recovers whatever was dropped.
			if !ok {
				watcher.Close()
				watcher = nil
			} else {
				log.Printf("%s: error watching %s: %v", method, watchDir, err)
			}
		case <-dpi.stop:
			log.Printf("%s: shutting down", method)
			return nil
		case <-done:
			log.Printf("%s: device plugin server was torn down, stopping health checks", method)
			return nil
		}
	}
}

// deviceNodePresent reports whether a device node exists. Errors other than
// "does not exist" are treated as present: a transient or permission-related
// stat failure must not mark every GPU on the node unhealthy.
func deviceNodePresent(method, devPath string) bool {
	_, err := os.Stat(devPath)
	if err == nil {
		return true
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}

	log.Printf("%s: unable to stat %s, assuming device is still present: %v", method, devPath, err)
	return true
}

func supportsIOMMUFD() (bool, error) {
	_, err := os.Stat(filepath.Join(rootPath, iommuDevicePath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
