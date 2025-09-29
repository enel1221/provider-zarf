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

package zarfpackage

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	"github.com/zarf-dev/zarf/src/pkg/state"

	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane/provider-zarf/apis/zarf/v1alpha1"
	"github.com/crossplane/provider-zarf/internal/zarfclient"
)

// Unlike many Kubernetes projects Crossplane does not use third party testing
// libraries, per the common Go test review comments. Crossplane encourages the
// use of table driven unit tests. The tests of the crossplane-runtime project
// are representative of the testing style Crossplane encourages.
//
// https://github.com/golang/go/wiki/TestComments
// https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md#contributing-code

func TestObserve(t *testing.T) {
	type fields struct {
		client zarfclient.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalObservation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"PackageNotInstalled": {
			reason: "Should return ResourceExists=false when package is not installed",
			fields: fields{
				client: &zarfclient.MockClient{
					MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
						return false, nil, nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source: "oci://defenseunicorns/packages/dos-games:1.0.0",
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists: false,
				},
			},
		},
		"PackageInstalled": {
			reason: "Should return ResourceExists=true and ResourceUpToDate=true when package is installed",
			fields: fields{
				client: &zarfclient.MockClient{
					MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
						return true, &state.DeployedPackage{Name: "dos-games"}, nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source: "oci://defenseunicorns/packages/dos-games:1.0.0",
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:    true,
					ResourceUpToDate:  true,
					ConnectionDetails: managed.ConnectionDetails{},
				},
			},
		},
		"CheckInstalledError": {
			reason: "Should return error when IsInstalled fails",
			fields: fields{
				client: &zarfclient.MockClient{
					MockIsInstalled: func(ctx context.Context, source, namespace string) (bool, *state.DeployedPackage, error) {
						return false, nil, errors.New("cluster connection failed")
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source: "oci://defenseunicorns/packages/dos-games:1.0.0",
						},
					},
				},
			},
			want: want{
				err: errors.New("failed to check if package is installed: cluster connection failed"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{client: tc.fields.client}
			got, err := e.Observe(tc.args.ctx, tc.args.mg)
			switch {
			case tc.want.err != nil && err == nil:
				t.Errorf("\n%s\ne.Observe(...): expected error but got nil\n", tc.reason)
			case tc.want.err == nil && err != nil:
				t.Errorf("\n%s\ne.Observe(...): expected no error but got: %v\n", tc.reason, err)
			case tc.want.err != nil && err != nil && !strings.Contains(err.Error(), "cluster connection failed"):
				t.Errorf("\n%s\ne.Observe(...): expected error containing 'cluster connection failed' but got: %v\n", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	type fields struct {
		client zarfclient.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		c   managed.ExternalCreation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"SuccessfulDeploy": {
			reason: "Should successfully deploy package",
			fields: fields{
				client: &zarfclient.MockClient{
					MockDeploy: func(ctx context.Context, opts zarfclient.DeployOptions) (zarfclient.Result, error) {
						return zarfclient.Result{PackageName: "dos-games"}, nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source:     "oci://defenseunicorns/packages/dos-games:1.0.0",
							Namespace:  "games",
							Components: []string{"dos-games"},
							Variables: map[string]string{
								"GAME_COUNT": "3",
							},
						},
					},
				},
			},
			want: want{
				c: managed.ExternalCreation{},
			},
		},
		"DeploymentFailure": {
			reason: "Should return error when deployment fails",
			fields: fields{
				client: &zarfclient.MockClient{
					MockDeploy: func(ctx context.Context, opts zarfclient.DeployOptions) (zarfclient.Result, error) {
						return zarfclient.Result{}, errors.New("deployment failed")
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source: "oci://defenseunicorns/packages/dos-games:1.0.0",
						},
					},
				},
			},
			want: want{
				err: errors.New("failed to deploy package: deployment failed"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{client: tc.fields.client}
			got, err := e.Create(tc.args.ctx, tc.args.mg)
			switch {
			case tc.want.err != nil && err == nil:
				t.Errorf("\n%s\ne.Create(...): expected error but got nil\n", tc.reason)
			case tc.want.err == nil && err != nil:
				t.Errorf("\n%s\ne.Create(...): expected no error but got: %v\n", tc.reason, err)
			case tc.want.err != nil && err != nil && !strings.Contains(err.Error(), "deployment failed"):
				t.Errorf("\n%s\ne.Create(...): expected error containing 'deployment failed' but got: %v\n", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want.c, got); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	type fields struct {
		client zarfclient.Client
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		d   managed.ExternalDelete
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"SuccessfulRemoval": {
			reason: "Should successfully remove package",
			fields: fields{
				client: &zarfclient.MockClient{
					MockRemove: func(ctx context.Context, packageName, namespace string) error {
						return nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source:    "oci://defenseunicorns/packages/dos-games:1.0.0",
							Namespace: "games",
						},
					},
				},
			},
			want: want{
				d: managed.ExternalDelete{},
			},
		},
		"RemovalFailure": {
			reason: "Should return error when removal fails",
			fields: fields{
				client: &zarfclient.MockClient{
					MockRemove: func(ctx context.Context, packageName, namespace string) error {
						return errors.New("removal failed")
					},
				},
			},
			args: args{
				ctx: context.Background(),
				mg: &v1alpha1.ZarfPackage{
					Spec: v1alpha1.ZarfPackageSpec{
						ForProvider: v1alpha1.ZarfPackageParameters{
							Source: "oci://defenseunicorns/packages/dos-games:1.0.0",
						},
					},
				},
			},
			want: want{
				err: errors.New("failed to remove package: removal failed"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{client: tc.fields.client}
			got, err := e.Delete(tc.args.ctx, tc.args.mg)
			switch {
			case tc.want.err != nil && err == nil:
				t.Errorf("\n%s\ne.Delete(...): expected error but got nil\n", tc.reason)
			case tc.want.err == nil && err != nil:
				t.Errorf("\n%s\ne.Delete(...): expected no error but got: %v\n", tc.reason, err)
			case tc.want.err != nil && err != nil && !strings.Contains(err.Error(), "removal failed"):
				t.Errorf("\n%s\ne.Delete(...): expected error containing 'removal failed' but got: %v\n", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want.d, got); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestExtractPackageName(t *testing.T) {
	cases := map[string]struct {
		source   string
		expected string
	}{
		"OCI with version": {
			source:   "oci://defenseunicorns/packages/dos-games:1.0.0",
			expected: "dos-games",
		},
		"OCI without version": {
			source:   "oci://registry1.dso.mil/ironbank/big-bang/bigbang",
			expected: "bigbang",
		},
		"Simple name": {
			source:   "dos-games",
			expected: "dos-games",
		},
		"File path": {
			source:   "/path/to/package.tar.zst",
			expected: "package.tar.zst",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := extractPackageName(tc.source)
			if got != tc.expected {
				t.Errorf("extractPackageName(%q) = %q, want %q", tc.source, got, tc.expected)
			}
		})
	}
}
