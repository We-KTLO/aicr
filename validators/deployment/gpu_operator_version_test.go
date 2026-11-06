// Copyright (c) 2026, NVIDIA CORPORATION & AFFILIATES.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/NVIDIA/aicr/pkg/recipe"
	v1 "github.com/NVIDIA/aicr/pkg/validator/v1"
	"github.com/NVIDIA/aicr/validators"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestExtractVersionFromImage(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  string
	}{
		{
			name:  "standard image with tag",
			image: "nvcr.io/nvidia/gpu-operator:v24.9.0",
			want:  "v24.9.0",
		},
		{
			name:  "tag without v prefix",
			image: "nvcr.io/nvidia/gpu-operator:24.9.0",
			want:  "v24.9.0",
		},
		{
			name:  "tag with suffix after dash",
			image: "nvcr.io/nvidia/gpu-operator:v24.9.0-ubi8",
			want:  "v24.9.0",
		},
		{
			name:  "tag with suffix no v prefix",
			image: "nvcr.io/nvidia/gpu-operator:24.9.0-ubi8",
			want:  "v24.9.0",
		},
		{
			name:  "no tag returns empty",
			image: "nvcr.io/nvidia/gpu-operator",
			want:  "",
		},
		{
			name:  "empty string returns empty",
			image: "",
			want:  "",
		},
		{
			name:  "registry with port",
			image: "host:5000/nvidia/gpu-operator:v1.0.0",
			want:  "v1.0.0",
		},
		{
			name:  "registry with port and no tag",
			image: "host:5000/nvidia/gpu-operator",
			want:  "",
		},
		{
			name:  "digest only",
			image: "nvcr.io/nvidia/gpu-operator@sha256:" + testDigest,
			want:  "",
		},
		{
			name:  "tag and digest",
			image: "nvcr.io/nvidia/gpu-operator:v25.10.1@sha256:" + testDigest,
			want:  "v25.10.1",
		},
		{
			name:  "registry with port, tag and digest",
			image: "host:5000/nvidia/gpu-operator:v25.10.1-ubi9@sha256:" + testDigest,
			want:  "v25.10.1",
		},
		{
			name:  "registry with port and digest only",
			image: "host:5000/nvidia/gpu-operator@sha256:" + testDigest,
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractVersionFromImage(tt.image)
			if got != tt.want {
				t.Errorf("extractVersionFromImage(%q) = %q, want %q", tt.image, got, tt.want)
			}
		})
	}
}

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{
			name:    "already has v prefix",
			version: "v24.9.0",
			want:    "v24.9.0",
		},
		{
			name:    "no v prefix",
			version: "24.9.0",
			want:    "v24.9.0",
		},
		{
			name:    "empty string",
			version: "",
			want:    "",
		},
		{
			name:    "whitespace only",
			version: "   ",
			want:    "",
		},
		{
			name:    "whitespace around version",
			version: "  24.9.0  ",
			want:    "v24.9.0",
		},
		{
			name:    "v prefix with whitespace",
			version: "  v1.2.3  ",
			want:    "v1.2.3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeVersion(tt.version)
			if got != tt.want {
				t.Errorf("normalizeVersion(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

func TestParseImageReference(t *testing.T) {
	tests := []struct {
		name       string
		image      string
		wantTag    string
		wantDigest string
	}{
		{name: "tag", image: "nvcr.io/nvidia/gpu-operator:v25.10.1", wantTag: "v25.10.1"},
		{name: "digest", image: "nvcr.io/nvidia/gpu-operator@sha256:" + testDigest, wantDigest: "sha256:" + testDigest},
		{name: "tag and digest", image: "nvcr.io/nvidia/gpu-operator:v25.10.1@sha256:" + testDigest, wantTag: "v25.10.1", wantDigest: "sha256:" + testDigest},
		{name: "port, no tag", image: "host:5000/gpu-operator"},
		{name: "port and digest", image: "host:5000/gpu-operator@sha256:" + testDigest, wantDigest: "sha256:" + testDigest},
		{name: "bare name", image: "gpu-operator"},
		{name: "empty", image: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tag, digest := parseImageReference(tt.image)
			if tag != tt.wantTag || digest != tt.wantDigest {
				t.Errorf("parseImageReference(%q) = (%q, %q), want (%q, %q)", tt.image, tag, digest, tt.wantTag, tt.wantDigest)
			}
		})
	}
}

func operatorDeployment(image string, labels, annotations map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        gpuOperatorNamespace,
			Namespace:   gpuOperatorNamespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "gpu-operator", Image: image}},
				},
			},
		},
	}
}

func TestGetGPUOperatorVersion(t *testing.T) {
	digestOnly := "nvcr.io/nvidia/gpu-operator@sha256:" + testDigest
	tests := []struct {
		name       string
		deployment *appsv1.Deployment
		wantVer    string
		wantPinned string
		wantErr    bool
	}{
		{
			name:       "tagged image",
			deployment: operatorDeployment("nvcr.io/nvidia/gpu-operator:v25.10.1", nil, nil),
			wantVer:    "v25.10.1",
		},
		{
			name:       "tag and digest reads the tag",
			deployment: operatorDeployment("nvcr.io/nvidia/gpu-operator:v25.10.1@sha256:"+testDigest, nil, nil),
			wantVer:    "v25.10.1",
		},
		{
			name:       "digest only is reported as pinned, not as a version",
			deployment: operatorDeployment(digestOnly, nil, nil),
			wantPinned: digestOnly,
		},
		{
			name:       "digest only with version label uses the label",
			deployment: operatorDeployment(digestOnly, map[string]string{"app.kubernetes.io/version": "v26.3.0"}, nil),
			wantVer:    "v26.3.0",
		},
		{
			name:       "digest only with version annotation uses the annotation",
			deployment: operatorDeployment(digestOnly, nil, map[string]string{"nvidia.com/gpu-operator-version": "26.3.0"}),
			wantVer:    "v26.3.0",
		},
		{
			name:       "untagged image without digest is an error",
			deployment: operatorDeployment("nvcr.io/nvidia/gpu-operator", nil, nil),
			wantErr:    true,
		},
		{
			name:    "no deployment is an error",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientset := k8sfake.NewClientset()
			if tt.deployment != nil {
				clientset = k8sfake.NewClientset(tt.deployment)
			}
			version, pinned, err := getGPUOperatorVersion(context.Background(), clientset)
			if (err != nil) != tt.wantErr {
				t.Fatalf("getGPUOperatorVersion() error = %v, wantErr %v", err, tt.wantErr)
			}
			if version != tt.wantVer || pinned != tt.wantPinned {
				t.Errorf("getGPUOperatorVersion() = (%q, %q), want (%q, %q)", version, pinned, tt.wantVer, tt.wantPinned)
			}
		})
	}
}

// TestCheckGPUOperatorVersion_DigestPinned covers a cluster whose operator
// image is pinned by digest alone: the check skips with version-unknown and
// never evaluates the digest against the constraint.
func TestCheckGPUOperatorVersion_DigestPinned(t *testing.T) {
	deploy := operatorDeployment("nvcr.io/nvidia/gpu-operator@sha256:"+testDigest, nil, nil)
	ctx := &validators.Context{
		Ctx:             context.Background(),
		Clientset:       k8sfake.NewClientset(deploy),
		ValidationInput: &v1.ValidationInput{},
	}
	ctx.ValidationInput.Config.Deployment = &v1.ValidationPhase{
		Constraints: []recipe.Constraint{{Name: "Deployment.gpu-operator.version", Value: ">= v25.10.0"}},
	}
	extra, err := captureExtras(t, func() error { return checkGPUOperatorVersion(ctx) })
	if !validators.IsSkip(err) {
		t.Fatalf("checkGPUOperatorVersion() error = %v, want a skip", err)
	}
	if !strings.Contains(err.Error(), "digest-pinned") {
		t.Errorf("skip reason %q does not mention digest-pinned", err.Error())
	}
	assertExtraSubset(t, extra, map[string]string{validators.ExtraKeySkipReason: validators.SkipReasonVersionUnknown})
	for _, key := range []string{validators.ExtraKeyDetectedVersion, validators.ExtraKeyVersionSatisfied} {
		if v, ok := extra[key]; ok {
			t.Errorf("%s = %q, want absent for a digest-pinned image", key, v)
		}
	}
}
