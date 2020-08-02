package controllers

import (
	"context"
	"testing"

	v1alpha1 "github.com/leg100/stok/api/v1alpha1"
	"github.com/leg100/stok/scheme"
	"github.com/operator-framework/operator-sdk/pkg/status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestReconcileWorkspaceStatus(t *testing.T) {
	plan1 := v1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{
			Name: "plan-1",
			Labels: map[string]string{
				"workspace": "workspace-1",
			},
		},
	}

	plan2 := v1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{
			Name: "plan-2",
			Labels: map[string]string{
				"workspace": "workspace-1",
			},
		},
	}

	plan3 := v1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{
			Name: "plan-3",
			Labels: map[string]string{
				"workspace": "workspace-2",
			},
		},
	}

	planCompleted := v1alpha1.Plan{
		ObjectMeta: metav1.ObjectMeta{
			Name: "plan-3",
			Labels: map[string]string{
				"workspace": "workspace-1",
			},
		},
		CommandStatus: v1alpha1.CommandStatus{
			Conditions: status.Conditions{
				status.Condition{
					Type:   v1alpha1.ConditionCompleted,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	tests := []struct {
		name       string
		workspace  *v1alpha1.Workspace
		objs       []runtime.Object
		assertions func(ws *v1alpha1.Workspace)
	}{
		{
			name: "Missing secret",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
				Spec: v1alpha1.WorkspaceSpec{
					SecretName: "stok",
				},
			},
			assertions: func(ws *v1alpha1.Workspace) {
				require.Equal(t, corev1.ConditionFalse, ws.Status.Conditions.GetCondition(v1alpha1.ConditionHealthy).Status)
			},
		},
		{
			name: "Missing service account",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
				Spec: v1alpha1.WorkspaceSpec{
					ServiceAccountName: "stok",
				},
			},
			assertions: func(ws *v1alpha1.Workspace) {
				require.Equal(t, corev1.ConditionFalse, ws.Status.Conditions.GetCondition(v1alpha1.ConditionHealthy).Status)
			},
		},
		{
			name: "No secret nor service account specified",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
			},
			assertions: func(ws *v1alpha1.Workspace) {
				require.Equal(t, corev1.ConditionTrue, ws.Status.Conditions.GetCondition(v1alpha1.ConditionHealthy).Status)
			},
		},
		{
			name: "No commands",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
			},
			assertions: func(ws *v1alpha1.Workspace) {
				require.Equal(t, []string{}, ws.Status.Queue)
			},
		},
		{
			name: "Single command",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
			},
			objs: []runtime.Object{
				runtime.Object(&plan1),
			},
			assertions: func(ws *v1alpha1.Workspace) {
				require.Equal(t, []string{"plan-1"}, ws.Status.Queue)
			},
		},
		{
			name: "Three commands, one of which is unrelated to this workspace",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
			},
			objs: []runtime.Object{
				runtime.Object(&plan1),
				runtime.Object(&plan2),
				runtime.Object(&plan3),
			},
			assertions: func(ws *v1alpha1.Workspace) {
				require.Equal(t, []string{"plan-1", "plan-2"}, ws.Status.Queue)
			},
		},
		{
			name: "Existing queue",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
				Status: v1alpha1.WorkspaceStatus{
					Queue: []string{
						"plan-1",
					},
				},
			},
			objs: []runtime.Object{
				runtime.Object(&plan1),
				runtime.Object(&plan2),
			},
			assertions: func(ws *v1alpha1.Workspace) {
				require.Equal(t, []string{"plan-1", "plan-2"}, ws.Status.Queue)
			},
		},
		{
			name: "Completed command",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
			},
			objs: []runtime.Object{
				runtime.Object(&planCompleted),
				runtime.Object(&plan1),
				runtime.Object(&plan2),
			},
			assertions: func(ws *v1alpha1.Workspace) {
				require.Equal(t, []string{"plan-1", "plan-2"}, ws.Status.Queue)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := append(tt.objs, runtime.Object(tt.workspace))
			cl := fake.NewFakeClientWithScheme(scheme.Scheme, objs...)

			r := &WorkspaceReconciler{
				Client: cl,
				Scheme: scheme.Scheme,
				Log:    ctrl.Log.WithName("controllers").WithName("Workspace"),
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.workspace.GetName(),
					Namespace: tt.workspace.GetNamespace(),
				},
			}
			_, err := r.Reconcile(req)
			require.NoError(t, err)

			err = r.Get(context.TODO(), req.NamespacedName, tt.workspace)
			require.NoError(t, err)

			tt.assertions(tt.workspace)
		})
	}
}

func TestReconcileWorkspacePVC(t *testing.T) {
	tests := []struct {
		name       string
		workspace  *v1alpha1.Workspace
		assertions func(pvc *corev1.PersistentVolumeClaim)
	}{
		{
			name: "Default size",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
			},
			assertions: func(pvc *corev1.PersistentVolumeClaim) {
				size := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
				require.Equal(t, "1Gi", size.String())
			},
		},
		{
			name: "Custom storage class",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
				Spec: v1alpha1.WorkspaceSpec{
					Cache: v1alpha1.WorkspaceCacheSpec{
						StorageClass: "local-path",
					},
				},
			},
			assertions: func(pvc *corev1.PersistentVolumeClaim) {
				require.Equal(t, "local-path", *pvc.Spec.StorageClassName)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewFakeClientWithScheme(scheme.Scheme, tt.workspace)

			r := NewWorkspaceReconciler(cl)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.workspace.GetName(),
					Namespace: tt.workspace.GetNamespace(),
				},
			}
			_, err := r.Reconcile(req)
			require.NoError(t, err)

			pvc := &corev1.PersistentVolumeClaim{}
			err = r.Get(context.TODO(), req.NamespacedName, pvc)
			require.NoError(t, err)

			tt.assertions(pvc)
		})
	}
}

func TestReconcileWorkspaceConfigMap(t *testing.T) {
	tests := []struct {
		name       string
		workspace  *v1alpha1.Workspace
		assertions func(configmap *corev1.ConfigMap)
	}{
		{
			name: "Default",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
				Spec: v1alpha1.WorkspaceSpec{
					Backend: v1alpha1.BackendSpec{
						Type: "local",
					},
				},
			},
			assertions: func(configmap *corev1.ConfigMap) {
				require.Equal(t, map[string]string{
					"backend.tf": `terraform {
  backend "local" {}
}
`,
					"backend.ini": "",
				}, configmap.Data)
			},
		},
		{
			name: "GCS backend",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
				Spec: v1alpha1.WorkspaceSpec{
					Backend: v1alpha1.BackendSpec{
						Type: "gcs",
						Config: map[string]string{
							"bucket": "workspace-1-state",
							"prefix": "dev",
						},
					},
				},
			},
			assertions: func(configmap *corev1.ConfigMap) {
				require.Equal(t, map[string]string{
					"backend.tf": `terraform {
  backend "gcs" {}
}
`,
					"backend.ini": `bucket	= "workspace-1-state"
prefix	= "dev"
`,
				}, configmap.Data)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewFakeClientWithScheme(scheme.Scheme, tt.workspace)

			r := NewWorkspaceReconciler(cl)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.workspace.GetName(),
					Namespace: tt.workspace.GetNamespace(),
				},
			}
			_, err := r.Reconcile(req)
			require.NoError(t, err)

			configmap := &corev1.ConfigMap{}
			configmapkey := types.NamespacedName{
				Name:      "workspace-" + tt.workspace.GetName(),
				Namespace: tt.workspace.GetNamespace(),
			}
			err = r.Get(context.TODO(), configmapkey, configmap)
			require.NoError(t, err)

			tt.assertions(configmap)
		})
	}
}

func TestReconcileWorkspacePod(t *testing.T) {
	tests := []struct {
		name       string
		workspace  *v1alpha1.Workspace
		objs       []runtime.Object
		assertions func(pod *corev1.Pod)
	}{
		{
			name: "Default",
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "workspace-1",
					Namespace: "controller-test",
				},
				Spec: v1alpha1.WorkspaceSpec{
					TimeoutClient: "10s",
				},
			},
			assertions: func(pod *corev1.Pod) {
				assert.Equal(t, []string{
					"--kind", "Workspace",
					"--name", "workspace-1",
					"--namespace", "controller-test",
					"--timeout", "10s",
					"--",
					"-backend-config=backend.ini"},
					pod.Spec.InitContainers[0].Args)
			},
		},
		{
			name: "With credentials",
			objs: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name: "stok",
					},
					StringData: map[string]string{
						"AWS_ACCESS_KEY_ID":                   "youraccesskeyid",
						"AWS_SECRET_ACCESS_KEY":               "yoursecretaccesskey",
						"google_application_credentials.json": "abc",
					},
				},
			},
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
				Spec: v1alpha1.WorkspaceSpec{
					SecretName: "stok",
				},
			},
			assertions: func(pod *corev1.Pod) {
				got, ok := getEnvValueForName(&pod.Spec.InitContainers[0], "GOOGLE_APPLICATION_CREDENTIALS")
				if !ok {
					t.Fatal("Could not find env var with name GOOGLE_APPLICATION_CREDENTIALS")
				}
				assert.Equal(t, "/credentials/google-credentials.json", got)

				assert.Equal(t, "stok", pod.Spec.InitContainers[0].EnvFrom[0].SecretRef.Name)
			},
		},
		{
			name: "With service account",
			objs: []runtime.Object{
				&corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name: "stok",
					},
				},
			},
			workspace: &v1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "workspace-1",
				},
				Spec: v1alpha1.WorkspaceSpec{
					ServiceAccountName: "stok",
				},
			},
			assertions: func(pod *corev1.Pod) {
				assert.Equal(t, "stok", pod.Spec.ServiceAccountName)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewFakeClientWithScheme(scheme.Scheme, append(tt.objs, tt.workspace)...)

			r := NewWorkspaceReconciler(cl)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.workspace.GetName(),
					Namespace: tt.workspace.GetNamespace(),
				},
			}
			_, err := r.Reconcile(req)
			require.NoError(t, err)

			pod := &corev1.Pod{}
			podkey := types.NamespacedName{
				Name:      "workspace-" + tt.workspace.GetName(),
				Namespace: tt.workspace.GetNamespace(),
			}
			err = r.Get(context.TODO(), podkey, pod)
			require.NoError(t, err)

			tt.assertions(pod)
		})
	}
}