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
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	resource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	ctxTimeout = 5 * time.Second
)

func getGFDImageName(clientset *kubernetes.Clientset, namespace string) string {
	// if there is an override on the image, then use that
	gfdImage := os.Getenv("GFD_IMAGE")
	if gfdImage != "" {
		return gfdImage
	}

	// else use self image
	ctx, cancel := context.WithTimeout(context.Background(), ctxTimeout)
	defer cancel()
	podName := os.Getenv("HOSTNAME")
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		log.Printf("Could not get self pod to obtain GFD_IMAGE")
		return ""
	}
	gfdImage = pod.Spec.Containers[0].Image
	log.Printf("Using %s as GFD Image", gfdImage)
	return gfdImage
}

func runGFD() {
	// 1. Get the Node Name from the environment (passed via Downward API)
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Printf("NODE_NAME environment variable is required for running GFD")
		return
	}
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		log.Printf("POD_NAMESPACE environment variable is required for running GFD")
		return
	}

	// 2. Authenticate within the cluster
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("Error obtaining cluster credentials for GFD launch: %v", err.Error())
		return
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("Error obtaining clientset for GFD launch: %v", err.Error())
		return
	}

	gfdImage := getGFDImageName(clientset, namespace)
	if gfdImage == "" {
		log.Printf("Error: No GFD Image available to run GFD")
	}

	err = WaitForKataRuntime(clientset, nodeName)
	if err != nil {
		log.Printf("Error waiting for Kata runtime to come up for GFD job: %v", err.Error())
		return
	}

	// 3. Create the gfd pod
	gfdPod := createGFDPod(clientset, nodeName, namespace, gfdImage)
	err = LaunchPodWithRetries(clientset, gfdPod, namespace)
	if err != nil {
		log.Printf("Error creating GFD pod: %v", err.Error())
		return
	}

	return
}

func createGFDPod(clientset *kubernetes.Clientset, nodeName, namespace, gfdImage string) *corev1.Pod {
	var trueValue bool = true
	var runtimeClassName string = "kata-qemu-nvidia-gpu"
	// check if this is an snp machine with ConfidentialContainers enabled
	exists, value := getNodeLabel(clientset, nodeName, "nvidia.com/cc.ready.state")
	if exists && strings.EqualFold(value, "true") {
		exists, value = getNodeLabel(clientset, nodeName, "amd.feature.node.kubernetes.io/snp")
		if exists && strings.EqualFold(value, "true") {
			runtimeClassName = "kata-qemu-nvidia-gpu-snp"
		} else {
			exists, value = getNodeLabel(clientset, nodeName, "intel.feature.node.kubernetes.io/tdx")
			if exists && strings.EqualFold(value, "true") {
				runtimeClassName = "kata-qemu-nvidia-gpu-tdx"
			}
		}
	}
	log.Printf("Runtime class for GFD pod: %s", runtimeClassName)

	resourceName := fmt.Sprintf("%s/%s", DeviceNamespace, getGPUDeviceName())
	gpuQuantity := resource.MustParse("1")

	// 3. Define the Pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("gfd-%s", nodeName),
		},
		Spec: corev1.PodSpec{
			NodeName:           nodeName, // This forces the pod to land on the specific node
			RestartPolicy:      corev1.RestartPolicyOnFailure,
			RuntimeClassName:   &runtimeClassName,
			ServiceAccountName: "nvidia-sandbox-device-plugin",
			Containers: []corev1.Container{
				{
					Name:    "gpu-feature-discovery",
					Image:   gfdImage,
					Command: []string{"/usr/bin/gpu-feature-discovery"},
					Env: []corev1.EnvVar{
						{Name: "MIG_STRATEGY", Value: "none"},
						{Name: "GFD_ONESHOT", Value: "true"},
						{Name: "GFD_USE_NODE_FEATURE_API", Value: "true"},
						{Name: "NODE_NAME", Value: nodeName},
						{Name: "NAMESPACE", Value: namespace},
					},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceName(resourceName): gpuQuantity,
						},
						Requests: corev1.ResourceList{
							corev1.ResourceName(resourceName): gpuQuantity,
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "output-dir",
							MountPath: "/etc/kubernetes/node-feature-discovery/features.d",
						},
						{
							Name:      "host-sys",
							MountPath: "/sys",
						},
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged: &trueValue,
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "output-dir",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/etc/kubernetes/node-feature-discovery/features.d",
						},
					},
				},
				{
					Name: "host-sys",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/sys",
						},
					},
				},
			},
		},
	}
	return pod
}

// getNodeLabel gets a specified label from the node. returns boolean(found/not-found),
// and string(value)
func getNodeLabel(clientset *kubernetes.Clientset, nodeName, labelKey string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), ctxTimeout)
	defer cancel()
	node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		// Returning (false, nil) tells the backoff to keep trying
		log.Printf("API Error fetching node: %v", err)
		return false, ""
	}

	val, exists := node.Labels[labelKey]
	if exists {
		log.Printf("Success: Label %s found!", labelKey)
		return true, val
	}
	return false, ""
}

// LaunchPodWithRetries creates the pod object with exponential backoff based retries
func LaunchPodWithRetries(clientset *kubernetes.Clientset, pod *corev1.Pod, namespace string) error {
	backoff := wait.Backoff{
		Duration: 1 * time.Second,  // Initial delay
		Factor:   1.5,              // Multiply delay by this factor each step
		Jitter:   0.1,              // Add random variation to avoid "thundering herd"
		Steps:    50,               // Total number of retries
		Cap:      30 * time.Second, // Maximum delay between any two attempts
	}

	// 2. Execute the retry logic
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), ctxTimeout)
		defer cancel()

		result, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
		if err != nil {
			// Returning (false, nil) tells the backoff to keep trying
			log.Printf("API Error creating pod object: %v. Retrying...\n", err)
			return false, nil
		}
		log.Printf("Pod %s created successfully.", result.Name)
		return true, nil // (true, nil) stops the loop successfully
	})
	return err
}

// WaitForKataRuntime
func WaitForKataRuntime(clientset *kubernetes.Clientset, nodeName string) error {
	kataRuntimeLabelKey := "katacontainers.io/kata-runtime"
	kataRuntimeLabelValue := "true"

	// 1. Define the Exponential Backoff parameters
	backoff := wait.Backoff{
		Duration: 1 * time.Second,  // Initial delay
		Factor:   1.5,              // Multiply delay by this factor each step
		Jitter:   0.1,              // Add random variation to avoid "thundering herd"
		Steps:    50,               // Total number of retries
		Cap:      30 * time.Second, // Maximum delay between any two attempts
	}

	log.Printf("Monitoring node %s with exponential backoff...\n", nodeName)

	// 2. Execute the retry logic
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), ctxTimeout)
		defer cancel()

		node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			// Returning (false, nil) tells the backoff to keep trying
			log.Printf("API Error fetching node: %v. Retrying...\n", err)
			return false, nil
		}

		val, exists := node.Labels[kataRuntimeLabelKey]
		if exists && val == kataRuntimeLabelValue {
			log.Printf("Success: Label %s found!", kataRuntimeLabelKey)
			return true, nil // (true, nil) stops the loop successfully
		}

		log.Printf("Label %s=%s not found yet. Backing off...\n", kataRuntimeLabelKey, kataRuntimeLabelValue)
		return false, nil
	})

	if err != nil {
		log.Printf("Finished: Could not find label after %d attempts. Error: %v\n", backoff.Steps, err)
	}
	return err
}

func getGPUDeviceName() string {
	for deviceID, _ := range deviceMap {
		// Determine device name - skip nvswitch
		var deviceName string
		if isNVSwitchDeviceID(deviceID) {
			continue
		}

		if PGPUAlias != "" {
			deviceName = PGPUAlias
		} else {
			deviceName = getDeviceNameForID(deviceID)
		}

		if deviceName == "" {
			log.Printf("Error: Could not find device name for device id: %s", deviceID)
			deviceName = deviceID
		}
		// return the first valid GPU device name, in case of heterogeneous nodes, this
		// will be insufficent, but we are limited by GFD labeling
		return deviceName
	}
	// this will cause a failure
	log.Printf("Error finding a suitable GPU device for GFD pod: %v", deviceMap)
	return ""
}
