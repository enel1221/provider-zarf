/*
Copyright 2025 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package zarfclient

import (
	"context"

	"github.com/zarf-dev/zarf/src/pkg/state"
)

// MockClient is a mock implementation of the Client interface for testing
type MockClient struct {
	MockDeploy      func(ctx context.Context, opts DeployOptions) (Result, error)
	MockRemove      func(ctx context.Context, packageName, namespace string) error
	MockIsInstalled func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error)
}

// Deploy calls the MockDeploy function if set, otherwise returns zero Result
func (m *MockClient) Deploy(ctx context.Context, opts DeployOptions) (Result, error) {
	if m.MockDeploy != nil {
		return m.MockDeploy(ctx, opts)
	}
	return Result{}, nil
}

// Remove calls the MockRemove function if set, otherwise returns nil
func (m *MockClient) Remove(ctx context.Context, packageName, namespace string) error {
	if m.MockRemove != nil {
		return m.MockRemove(ctx, packageName, namespace)
	}
	return nil
}

// IsInstalled calls the MockIsInstalled function if set, otherwise returns false, nil, nil
func (m *MockClient) IsInstalled(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
	if m.MockIsInstalled != nil {
		return m.MockIsInstalled(ctx, source, namespace)
	}
	return false, nil, nil
}
