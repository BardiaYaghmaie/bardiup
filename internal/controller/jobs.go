package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
	"github.com/BardiaYaghmaie/bardiup/internal/agent"
)

// JobSettings are controller-wide defaults for mover Jobs.
type JobSettings struct {
	Image           string
	ImagePullPolicy corev1.PullPolicy
}

type moverKind string

const (
	moverBackup  moverKind = "backup"
	moverRestore moverKind = "restore"
)

type moverJob struct {
	kind      moverKind
	name      string
	namespace string
	pvc       string
	owner     metav1.OwnerReference
	cfg       agent.Config
	storage   v1alpha1.S3Storage
	opts      v1alpha1.JobOptions
	node      string // pin to this node when non-empty
}

var errRWOPInUse = errors.New("volume uses ReadWriteOncePod and is mounted by a running pod; use method: snapshot or stop the workload")

// placement decides whether a mover pod must run on a specific node to share a ReadWriteOnce volume.
func placement(ctx context.Context, r client.Reader, pvc *corev1.PersistentVolumeClaim) (string, error) {
	modes := pvc.Spec.AccessModes
	if slices.Contains(modes, corev1.ReadWriteMany) || slices.Contains(modes, corev1.ReadOnlyMany) {
		return "", nil
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(pvc.Namespace)); err != nil {
		return "", fmt.Errorf("list pods: %w", err)
	}
	for _, p := range pods.Items {
		if p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		if p.Labels[v1alpha1.LabelManagedBy] == v1alpha1.ManagedByValue {
			continue
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == pvc.Name {
				if slices.Contains(modes, corev1.ReadWriteOncePod) {
					return "", errRWOPInUse
				}
				return p.Spec.NodeName, nil
			}
		}
	}
	return "", nil
}

// checkPodSecurity fails fast when the default (root) mover would be rejected by the namespace's
// Pod Security admission, instead of leaving a Job that can never start a pod.
func checkPodSecurity(ctx context.Context, r client.Reader, namespace string, opts v1alpha1.JobOptions) error {
	if opts.SecurityContext != nil {
		return nil
	}
	nsObj := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: namespace}, nsObj); err != nil {
		return client.IgnoreNotFound(err)
	}
	if nsObj.Labels["pod-security.kubernetes.io/enforce"] == "restricted" {
		return fmt.Errorf("namespace %s enforces the restricted Pod Security Standard, which forbids the default root mover; "+
			"set spec.job.securityContext to run as the user owning the volume's files", namespace)
	}
	return nil
}

func defaultSecurityContext(kind moverKind) *corev1.SecurityContext {
	caps := []corev1.Capability{"DAC_OVERRIDE"}
	if kind == moverRestore {
		caps = []corev1.Capability{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID"}
	}
	return &corev1.SecurityContext{
		RunAsUser:                ptr.To[int64](0),
		RunAsNonRoot:             ptr.To(false),
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: caps},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func defaultResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
	}
}

func (s JobSettings) build(m moverJob) (*batchv1.Job, error) {
	cfgJSON, err := json.Marshal(m.cfg)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{
		v1alpha1.LabelManagedBy: v1alpha1.ManagedByValue,
		v1alpha1.LabelComponent: string(m.kind),
	}

	sc := defaultSecurityContext(m.kind)
	if m.opts.SecurityContext != nil {
		sc = m.opts.SecurityContext.DeepCopy()
	}
	res := defaultResources()
	if m.opts.Resources != nil {
		res = *m.opts.Resources.DeepCopy()
	}

	container := corev1.Container{
		Name:                     "mover",
		Image:                    s.Image,
		ImagePullPolicy:          s.ImagePullPolicy,
		Args:                     []string{"agent", string(m.kind)},
		Env:                      []corev1.EnvVar{{Name: agent.ConfigEnv, Value: string(cfgJSON)}},
		VolumeMounts:             []corev1.VolumeMount{{Name: "data", MountPath: agent.DataDir, ReadOnly: m.kind == moverBackup}},
		SecurityContext:          sc,
		Resources:                res,
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
	}
	if ref := m.storage.CredentialsSecretRef; ref != nil && ref.Name != "" {
		container.EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: *ref}}}
	}

	pod := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: m.opts.ServiceAccountName,
		Containers:         []corev1.Container{container},
		Volumes: []corev1.Volume{{
			Name: "data",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: m.pvc,
				ReadOnly:  m.kind == moverBackup,
			}},
		}},
		Tolerations:  m.opts.Tolerations,
		NodeSelector: m.opts.NodeSelector,
	}
	if m.opts.ServiceAccountName == "" {
		pod.AutomountServiceAccountToken = ptr.To(false)
	}
	if m.node != "" {
		pod.NodeSelector = nil
		pod.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{m.node},
				}}}},
			},
		}}
		// The workload already runs there; follow it onto tainted nodes.
		pod.Tolerations = append(slices.Clone(pod.Tolerations), corev1.Toleration{Operator: corev1.TolerationOpExists})
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            m.name,
			Namespace:       m.namespace,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{m.owner},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(ptr.Deref(m.opts.BackoffLimit, 2)),
			ActiveDeadlineSeconds:   ptr.To(ptr.Deref(m.opts.ActiveDeadlineSeconds, 21600)),
			TTLSecondsAfterFinished: ptr.To[int32](86400),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       pod,
			},
		},
	}, nil
}

// jobOutcome reports whether a mover Job finished. On success it returns the agent result;
// on failure a human readable reason.
func jobOutcome(ctx context.Context, r client.Reader, job *batchv1.Job) (done bool, res *agent.Result, failure string) {
	var succeeded bool
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			done, succeeded = true, true
		case batchv1.JobFailed, batchv1.JobFailureTarget:
			// FailureTarget is set as soon as the Job has failed; Failed only follows once all its
			// pods are gone, which never happens while a pod is stuck on a lost node.
			done, failure = true, c.Message
		}
	}
	if !done {
		return false, nil, ""
	}
	res, raw := lastTermination(ctx, r, job)
	switch {
	case succeeded && res != nil && res.SHA256 != "":
		return true, res, ""
	case succeeded:
		failure = fmt.Sprintf("mover reported no result: %q", raw)
	case res != nil && res.Error != "":
		failure = res.Error
	case raw != "":
		failure = strings.TrimSpace(raw)
	}
	return true, nil, fmt.Sprintf("%s (job %s)", failure, job.Name)
}

// lastTermination reads the termination message of the newest finished mover pod of a Job.
func lastTermination(ctx context.Context, r client.Reader, job *batchv1.Job) (*agent.Result, string) {
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(job.Namespace), client.MatchingLabels{batchv1.JobNameLabel: job.Name}); err != nil {
		return nil, ""
	}
	var newest *corev1.ContainerStateTerminated
	for i := range pods.Items {
		for _, cs := range pods.Items[i].Status.ContainerStatuses {
			if t := cs.State.Terminated; t != nil && (newest == nil || t.FinishedAt.After(newest.FinishedAt.Time)) {
				newest = t
			}
		}
	}
	if newest == nil {
		return nil, ""
	}
	res, err := agent.ParseResult(newest.Message)
	if err != nil {
		return nil, newest.Message
	}
	return res, newest.Message
}

func ownerRef(obj client.Object, kind string) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         v1alpha1.GroupVersion.String(),
		Kind:               kind,
		Name:               obj.GetName(),
		UID:                obj.GetUID(),
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}
