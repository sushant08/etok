package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leg100/etok/api/etok.dev/v1alpha1"
	"github.com/leg100/etok/cmd/flags"
	cmdutil "github.com/leg100/etok/cmd/util"
	"github.com/leg100/etok/pkg/client"
	"github.com/leg100/etok/pkg/controllers"
	"github.com/leg100/etok/pkg/handlers"
	"github.com/leg100/etok/pkg/k8s"
	"github.com/leg100/etok/pkg/labels"
	"github.com/leg100/etok/pkg/monitors"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/leg100/etok/pkg/env"
	"github.com/leg100/etok/pkg/logstreamer"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/klog/v2"
)

const (
	defaultReconcileTimeout   = 10 * time.Second
	defaultPodTimeout         = 60 * time.Second
	defaultRestoreTimeout     = 60 * time.Second
	defaultCacheSize          = "1Gi"
	defaultSecretName         = "etok"
	defaultServiceAccountName = "etok"
)

var (
	errPodTimeout       = errors.New("timed out waiting for pod to be ready")
	errReconcileTimeout = errors.New("timed out waiting for workspace to be reconciled")
	errRestoreTimeout   = errors.New("timed out waiting for workspace to provide status of restore")
	errWorkspaceNameArg = errors.New("expected single argument providing the workspace name")
)

type newOptions struct {
	*cmdutil.Factory

	*client.Client

	path        string
	namespace   string
	workspace   string
	kubeContext string

	// etok Workspace's workspaceSpec
	workspaceSpec v1alpha1.WorkspaceSpec
	// Create a service acccount if it does not exist
	disableCreateServiceAccount bool
	// Create a secret if it does not exist
	disableCreateSecret bool

	// Timeout for resource to be reconciled (at least once)
	reconcileTimeout time.Duration

	// Timeout for workspace pod to be ready
	podTimeout time.Duration

	// Timeout for workspace restore failure condition to report either true or
	// false (did the restore fail or not?).
	restoreTimeout time.Duration

	// Disable default behaviour of deleting resources upon error
	disableResourceCleanup bool

	// Recall if resources are created so that if error occurs they can be
	// cleaned up
	createdWorkspace      bool
	createdServiceAccount bool
	createdSecret         bool

	// Annotations to add to the service account resource
	serviceAccountAnnotations map[string]string

	// For testing purposes set workspace status
	status *v1alpha1.WorkspaceStatus

	variables            map[string]string
	environmentVariables map[string]string

	// backupBucket is the bucket to which the state file will backed up to
	backupBucket string

	etokenv *env.Env
}

func newCmd(f *cmdutil.Factory) (*cobra.Command, *newOptions) {
	o := &newOptions{
		Factory:   f,
		namespace: defaultNamespace,
	}
	cmd := &cobra.Command{
		Use:   "new <workspace>",
		Short: "Create a new etok workspace",
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			if len(args) != 1 {
				return errWorkspaceNameArg
			}

			o.workspace = args[0]

			o.etokenv, err = env.New(o.namespace, o.workspace)
			if err != nil {
				return err
			}

			// Storage class default is nil not empty string (pflags doesn't
			// permit default of nil)
			if !flags.IsFlagPassed(cmd.Flags(), "storage-class") {
				o.workspaceSpec.Cache.StorageClass = nil
			}

			o.Client, err = f.Create(o.kubeContext)
			if err != nil {
				return err
			}

			err = o.run(cmd.Context())
			if err != nil {
				if !o.disableResourceCleanup {
					o.cleanup()
				}
			}
			return err
		},
	}

	flags.AddPathFlag(cmd, &o.path)
	flags.AddNamespaceFlag(cmd, &o.namespace)
	flags.AddKubeContextFlag(cmd, &o.kubeContext)
	flags.AddDisableResourceCleanupFlag(cmd, &o.disableResourceCleanup)

	cmd.Flags().BoolVar(&o.disableCreateServiceAccount, "no-create-service-account", o.disableCreateServiceAccount, "Disable creation of service account")
	cmd.Flags().BoolVar(&o.disableCreateSecret, "no-create-secret", o.disableCreateSecret, "Disable creation of secret")

	cmd.Flags().StringVar(&o.workspaceSpec.ServiceAccountName, "service-account", defaultServiceAccountName, "Name of ServiceAccount")
	cmd.Flags().StringVar(&o.workspaceSpec.SecretName, "secret", defaultSecretName, "Name of Secret containing credentials")
	cmd.Flags().StringVar(&o.workspaceSpec.Cache.Size, "size", defaultCacheSize, "Size of PersistentVolume for cache")
	cmd.Flags().StringVar(&o.workspaceSpec.TerraformVersion, "terraform-version", "", "Override terraform version")
	cmd.Flags().StringVar(&o.workspaceSpec.BackupBucket, "backup-bucket", "", "Backup state to GCS bucket")

	// We want nil to be the default but it doesn't seem like pflags supports
	// that so use empty string and override later (see above)
	o.workspaceSpec.Cache.StorageClass = cmd.Flags().String("storage-class", "", "StorageClass of PersistentVolume for cache")

	cmd.Flags().DurationVar(&o.reconcileTimeout, "reconcile-timeout", defaultReconcileTimeout, "timeout for resource to be reconciled")
	cmd.Flags().DurationVar(&o.podTimeout, "pod-timeout", defaultPodTimeout, "timeout for pod to be ready")
	cmd.Flags().DurationVar(&o.restoreTimeout, "restore-timeout", defaultRestoreTimeout, "timeout for restore condition to report back")

	cmd.Flags().StringSliceVar(&o.workspaceSpec.PrivilegedCommands, "privileged-commands", []string{}, "Set privileged commands")

	cmd.Flags().StringToStringVar(&o.variables, "variables", map[string]string{}, "Set terraform variables")
	cmd.Flags().StringToStringVar(&o.environmentVariables, "environment-variables", map[string]string{}, "Set environment variables")
	cmd.Flags().StringToStringVar(&o.serviceAccountAnnotations, "sa-annotations", map[string]string{}, "Annotations to add to the etok ServiceAccount. Add iam.gke.io/gcp-service-account=[GSA_NAME]@[PROJECT_NAME].iam.gserviceaccount.com for workload identity")

	return cmd, o
}

func (o *newOptions) run(ctx context.Context) error {
	if !o.disableCreateServiceAccount {
		if err := o.createServiceAccountIfMissing(ctx); err != nil {
			return err
		}
	}

	if !o.disableCreateSecret {
		if err := o.createSecretIfMissing(ctx); err != nil {
			return err
		}
	}

	ws, err := o.createWorkspace(ctx)
	if err != nil {
		return err
	}
	o.createdWorkspace = true
	fmt.Fprintf(o.Out, "Created workspace %s\n", klog.KObj(ws))

	g, gctx := errgroup.WithContext(ctx)

	fmt.Fprintln(o.Out, "Waiting for workspace pod to be ready...")
	podch := make(chan *corev1.Pod, 1)
	g.Go(func() error {
		return o.waitForContainer(gctx, ws, podch)
	})

	if o.workspaceSpec.BackupBucket != "" {
		g.Go(func() error {
			return o.waitForRestore(gctx, ws)
		})
	}

	// Wait for resource to have been successfully reconciled at least once
	// within the ReconcileTimeout (If we don't do this and the operator is
	// either not installed or malfunctioning then the user would be none the
	// wiser until the much longer PodTimeout had expired).
	g.Go(func() error {
		return o.waitForReconcile(gctx, ws)
	})

	// Wait for workspace to have been reconciled and for its pod container to
	// be ready, and optionally, for restore status (if backup bucket
	// specified).
	if err := g.Wait(); err != nil {
		return err
	}

	// Receive ready pod
	pod := <-podch

	// Monitor exit code; non-blocking
	exit := monitors.ExitMonitor(ctx, o.KubeClient, pod.Name, pod.Namespace, controllers.InstallerContainerName)

	if err := logstreamer.Stream(ctx, o.GetLogsFunc, o.Out, o.PodsClient(o.namespace), ws.PodName(), controllers.InstallerContainerName); err != nil {
		return err
	}

	if err := o.etokenv.Write(o.path); err != nil {
		return err
	}

	// Return container's exit code
	select {
	case <-time.After(10 * time.Second):
		return fmt.Errorf("timed out waiting for exit code")
	case code := <-exit:
		return code
	}
}

func (o *newOptions) cleanup() {
	if o.createdWorkspace {
		o.WorkspacesClient(o.namespace).Delete(context.Background(), o.workspace, metav1.DeleteOptions{})
	}
	if o.createdSecret {
		o.SecretsClient(o.namespace).Delete(context.Background(), o.workspaceSpec.SecretName, metav1.DeleteOptions{})
	}
	if o.createdServiceAccount {
		o.ServiceAccountsClient(o.namespace).Delete(context.Background(), o.workspaceSpec.ServiceAccountName, metav1.DeleteOptions{})
	}
}

func (o *newOptions) createWorkspace(ctx context.Context) (*v1alpha1.Workspace, error) {
	ws := &v1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.workspace,
			Namespace: o.namespace,
		},
		Spec: o.workspaceSpec,
	}

	// Set etok's common labels
	labels.SetCommonLabels(ws)
	// Permit filtering secrets by workspace
	labels.SetLabel(ws, labels.Workspace(o.workspace))
	// Permit filtering etok resources by component
	labels.SetLabel(ws, labels.WorkspaceComponent)

	ws.Spec.Verbosity = o.Verbosity

	if o.status != nil {
		// For testing purposes seed workspace status
		ws.Status = *o.status
	}

	for k, v := range o.variables {
		ws.Spec.Variables = append(ws.Spec.Variables, &v1alpha1.Variable{Key: k, Value: v})
	}

	for k, v := range o.environmentVariables {
		ws.Spec.Variables = append(ws.Spec.Variables, &v1alpha1.Variable{Key: k, Value: v, EnvironmentVariable: true})
	}

	return o.WorkspacesClient(o.namespace).Create(ctx, ws, metav1.CreateOptions{})
}

func (o *newOptions) createSecretIfMissing(ctx context.Context) error {
	// Shorter var name for readability
	name := o.workspaceSpec.SecretName

	// Check if it exists already
	_, err := o.SecretsClient(o.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			_, err := o.createSecret(ctx, name)
			if err != nil {
				return fmt.Errorf("attempted to create secret: %w", err)
			}
			o.createdSecret = true
		} else {
			return fmt.Errorf("attempted to retrieve secret: %w", err)
		}
	}
	return nil
}

func (o *newOptions) createServiceAccountIfMissing(ctx context.Context) error {
	// Shorter var name for readability
	name := o.workspaceSpec.ServiceAccountName

	// Check if it exists already
	_, err := o.ServiceAccountsClient(o.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			_, err := o.createServiceAccount(ctx, name, o.serviceAccountAnnotations)
			if err != nil {
				return fmt.Errorf("attempted to create service account: %w", err)
			}
			o.createdServiceAccount = true
		} else {
			return fmt.Errorf("attempted to retrieve service account: %w", err)
		}
	}
	return nil
}

func (o *newOptions) createSecret(ctx context.Context, name string) (*corev1.Secret, error) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	// Set etok's common labels
	labels.SetCommonLabels(secret)
	// Permit filtering secrets by workspace
	labels.SetLabel(secret, labels.Workspace(o.workspace))
	// Permit filtering etok resources by component
	labels.SetLabel(secret, labels.WorkspaceComponent)

	return o.SecretsClient(o.namespace).Create(ctx, secret, metav1.CreateOptions{})
}

func (o *newOptions) createServiceAccount(ctx context.Context, name string, annotations map[string]string) (*corev1.ServiceAccount, error) {
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
	}
	// Set etok's common labels
	labels.SetCommonLabels(serviceAccount)
	// Permit filtering service accounts by workspace
	labels.SetLabel(serviceAccount, labels.Workspace(o.workspace))
	// Permit filtering etok resources by component
	labels.SetLabel(serviceAccount, labels.WorkspaceComponent)

	return o.ServiceAccountsClient(o.namespace).Create(ctx, serviceAccount, metav1.CreateOptions{})
}

// waitForContainer returns true once the installer container can be streamed
// from
func (o *newOptions) waitForContainer(ctx context.Context, ws *v1alpha1.Workspace, podch chan<- *corev1.Pod) error {
	lw := &k8s.PodListWatcher{Client: o.KubeClient, Name: ws.PodName(), Namespace: ws.Namespace}
	hdlr := handlers.ContainerReady(ws.PodName(), controllers.InstallerContainerName, true, false)

	ctx, cancel := context.WithTimeout(ctx, o.podTimeout)
	defer cancel()

	event, err := watchtools.UntilWithSync(ctx, lw, &corev1.Pod{}, nil, hdlr)
	if err != nil {
		if errors.Is(err, wait.ErrWaitTimeout) {
			return errPodTimeout
		}
		return err
	}
	podch <- event.Object.(*corev1.Pod)
	return nil
}

// waitForReconcile waits for the workspace resource to be reconciled.
func (o *newOptions) waitForReconcile(ctx context.Context, ws *v1alpha1.Workspace) error {
	lw := &k8s.WorkspaceListWatcher{Client: o.EtokClient, Name: ws.Name, Namespace: ws.Namespace}
	hdlr := handlers.Reconciled(ws)

	ctx, cancel := context.WithTimeout(ctx, o.reconcileTimeout)
	defer cancel()

	_, err := watchtools.UntilWithSync(ctx, lw, &v1alpha1.Workspace{}, nil, hdlr)
	if err != nil {
		if errors.Is(err, wait.ErrWaitTimeout) {
			return errReconcileTimeout
		}
		return err
	}
	return nil
}

// waitForRestore waits for the restoreFailure condition to provide info on
// restore of state file.
func (o *newOptions) waitForRestore(ctx context.Context, ws *v1alpha1.Workspace) error {
	lw := &k8s.WorkspaceListWatcher{Client: o.EtokClient, Name: ws.Name, Namespace: ws.Namespace}
	hdlr := handlers.Restore(o.Out)

	ctx, cancel := context.WithTimeout(ctx, o.restoreTimeout)
	defer cancel()

	_, err := watchtools.UntilWithSync(ctx, lw, &v1alpha1.Workspace{}, nil, hdlr)
	if err != nil {
		if errors.Is(err, wait.ErrWaitTimeout) {
			return errRestoreTimeout
		}
		return err
	}
	return nil
}
