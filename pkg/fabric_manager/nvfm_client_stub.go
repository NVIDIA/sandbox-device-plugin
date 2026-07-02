//go:build !nvfm || !linux || !cgo

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
	"context"
	"errors"
)

// Built reports whether this binary was compiled with libnvfm support. It is
// false in the stub build so callers can refuse to enable fabric manager
// instead of silently advertising a capability that always fails.
const Built = false

// ErrNotBuilt is returned by the stub client used when the binary was built
// without the nvfm build tag (i.e. without the Fabric Manager SDK).
var ErrNotBuilt = errors.New("fabric_manager: built without nvfm support (rebuild on linux with CGO_ENABLED=1 and -tags=nvfm)")

type stubClient struct{}

// NewFMClient returns a stub FMClient that fails all calls with ErrNotBuilt.
// The real libnvfm-backed client is only available when built with
// -tags=nvfm on linux.
func NewFMClient(cfg Config) FMClient {
	return stubClient{}
}

func (stubClient) GetSupportedPartitions(ctx context.Context) ([]Partition, error) {
	return nil, ErrNotBuilt
}
