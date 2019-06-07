package provisioner

import (
	"fmt"
	"strconv"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/yard-turkey/lib-bucket-provisioner/pkg/client/clientset/versioned"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/yard-turkey/lib-bucket-provisioner/pkg/apis/objectbucket.io/v1alpha1"
	"github.com/yard-turkey/lib-bucket-provisioner/pkg/provisioner/api"
)

const (
	// defaultRetryBaseInterval controls how long to wait for a single create API object call
	defaultRetryBaseInterval = time.Second * 3
	// defaultRetryTimeout defines how long in total to try to create an API object before ending the reconciliation
	// attempt
	defaultRetryTimeout = time.Second * 30

	bucketName      = "BUCKET_NAME"
	bucketHost      = "BUCKET_HOST"
	bucketPort      = "BUCKET_PORT"
	bucketRegion    = "BUCKET_REGION"
	bucketSubRegion = "BUCKET_SUBREGION"
	bucketSSL       = "BUCKET_SSL"

	// finalizer is applied to all resources generated by the provisioner
	finalizer = api.Domain + "/finalizer"

	objectBucketNameFormat = "obc-%s-%s"
)

// newBucketConfigMap returns a config map from a given endpoint and ObjectBucketClaim. 
// A finalizer is added to reduce chances of the CM being accidentally deleted. An OwnerReference
// is added so that the CM is automatically garbage collected when the parent OBC is deleted.
func newBucketConfigMap(ep *v1alpha1.Endpoint, obc *v1alpha1.ObjectBucketClaim) (*corev1.ConfigMap, error) {

	logD.Info("defining new configMap", "for claim", obc.Namespace+"/"+obc.Name)
	if ep == nil {
		return nil, fmt.Errorf("cannot construct configMap, got nil Endpoint")
	}
	if obc == nil {
		return nil, fmt.Errorf("cannot construct configMap, got nil OBC")
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       obc.Name,
			Namespace:  obc.Namespace,
			Finalizers: []string{finalizer},
			OwnerReferences: []metav1.OwnerReference{
				makeOwnerReference(obc),
			},
		},
		Data: map[string]string{
			bucketName:      ep.BucketName,
			bucketHost:      ep.BucketHost,
			bucketPort:      strconv.Itoa(ep.BucketPort),
			bucketSSL:       strconv.FormatBool(ep.SSL),
			bucketRegion:    ep.Region,
			bucketSubRegion: ep.SubRegion,
		},
	}, nil
}

// newCredentialsSecret returns a secret with data appropriate to the supported authenticaion
// method. Even if the values for the Authentication keys are empty, we generate the secret.
// A finalizer is added to reduce chances of the secret being accidentally deleted.
// An OwnerReference is added so that the secret is automatically garbage collected when the
// parent OBC is deleted.
func newCredentialsSecret(obc *v1alpha1.ObjectBucketClaim, auth *v1alpha1.Authentication) (*corev1.Secret, error) {

	if obc == nil {
		return nil, fmt.Errorf("ObjectBucketClaim required to generate secret")
	}
	if auth == nil {
		return nil, fmt.Errorf("got nil authentication, nothing to do")
	}
logD.Info("DEBUG *********", "obc meta", obc.ObjectMeta)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:       obc.Name,
			Namespace:  obc.Namespace,
			Finalizers: []string{finalizer},
			OwnerReferences: []metav1.OwnerReference{
				makeOwnerReference(obc),
			},
		},
	}

	secret.StringData = auth.ToMap()
logD.Info("DEBUG *********", "secret meta", secret.ObjectMeta)
	return secret, nil
}

// createObjectBucket creates an OB based on the passed-in ob spec.
// Note: a finalizer has been added to reduce chances of the ob being accidentally deleted.
func createObjectBucket(ob *v1alpha1.ObjectBucket, c versioned.Interface, retryInterval, retryTimeout time.Duration) (*v1alpha1.ObjectBucket, error) {
	logD.Info("creating ObjectBucket", "name", ob.Name)

	err := wait.PollImmediate(retryInterval, retryTimeout, func() (done bool, err error) {
		ob, err = c.ObjectbucketV1alpha1().ObjectBuckets().Create(ob)
		if err != nil {
			if errors.IsAlreadyExists(err) {
				// The object already exists don't spam the logs, instead let the request be requeued
				return true, err
			}
			// The error could be intermittent, log and try again
			log.Error(err, "probably not fatal, retrying")
			return false, nil
		}
		return true, nil
	})
	return ob, err
}

func createSecret(obc *v1alpha1.ObjectBucketClaim, auth *v1alpha1.Authentication, c kubernetes.Interface, retryInterval, retryTimeout time.Duration) (*corev1.Secret, error) {
	secret, err := newCredentialsSecret(obc, auth)
	if err != nil {
		return nil, err
	}

	err = wait.PollImmediate(retryInterval, retryTimeout, func() (done bool, err error) {
		secret, err = c.CoreV1().Secrets(obc.Namespace).Create(secret)
		if err != nil {
			if errors.IsAlreadyExists(err) {
				// The object already exists don't spam the logs, instead let the request be requeued
				return true, err
			}
			// The error could be intermittent, log and try again
			log.Error(err, "probably not fatal, retrying")
			return false, nil
		}
		return true, nil
	})
	return secret, err
}

func createConfigMap(obc *v1alpha1.ObjectBucketClaim, ep *v1alpha1.Endpoint, c kubernetes.Interface, retryInterval, retryTimeout time.Duration) (*corev1.ConfigMap, error) {
	configMap, err := newBucketConfigMap(ep, obc)
	if err != nil {
		return nil, err
	}

	err = wait.PollImmediate(retryInterval, retryTimeout, func() (done bool, err error) {
		configMap, err = c.CoreV1().ConfigMaps(obc.Namespace).Create(configMap)
		if err != nil {
			if errors.IsAlreadyExists(err) {
				// The object already exists don't spam the logs, instead let the request be requeued
				return true, err
			}
			// The error could be intermittent, log and try again
			log.Error(err, "probably not fatal, retrying")
			return false, nil
		}
		return true, nil
	})
	return configMap, err
}

// Only the finalizer needs to be removed. The CM will be garbage collected since its
// ownerReference refers to the parent OBC.
func releaseConfigMap(cm *corev1.ConfigMap, c kubernetes.Interface) error {
	if cm == nil {
		return nil
	}

	logD.Info("ConfigMap is garbage collected after its finalizer is removed", "name", cm.Namespace+"/"+cm.Name)
	removeFinalizer(cm)
	cm, err := c.CoreV1().ConfigMaps(cm.Namespace).Update(cm)
	if err != nil {
		return err
	}

	return nil
}

// Only the finalizer needs to be removed. The Secret will be garbage collected since its
// ownerReference refers to the parent OBC.
func releaseSecret(sec *corev1.Secret, c kubernetes.Interface) error {
	if sec == nil {
		log.Info("got nil secret, skipping")
		return nil
	}

	logD.Info("secret is garbage collected after its finalizer is removed", "name", sec.Namespace+"/"+sec.Name)
	removeFinalizer(sec)
	sec, err := c.CoreV1().Secrets(sec.Namespace).Update(sec)
	if err != nil {
		return err
	}

	return nil
}

// The OB does not have an ownerReference and must be explicitly deleted after its
// finalizer is removed.
func deleteObjectBucket(ob *v1alpha1.ObjectBucket, c versioned.Interface) error {
	if ob == nil {
		return nil
	}

	logD.Info("deleting OB after its finalizer is removed", "name", ob.Name)
	removeFinalizer(ob)
	ob, err := c.ObjectbucketV1alpha1().ObjectBuckets().Update(ob)
	if err != nil {
		return err
	}

	err = c.ObjectbucketV1alpha1().ObjectBuckets().Delete(ob.Name, &metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "ObjectBucket vanished before we could delete it, skipping", "ob", ob.Name)
			return nil
		}
		return fmt.Errorf("error deleting ObjectBucket %q: %v", ob.Name, err)
	}

	return nil
}

func updateClaim(c versioned.Interface, obc *v1alpha1.ObjectBucketClaim, retryInterval, retryTimeout time.Duration) (*v1alpha1.ObjectBucketClaim, error) {
	err := wait.PollImmediate(retryInterval, retryTimeout, func() (done bool, err error) {
		obc, err = c.ObjectbucketV1alpha1().ObjectBucketClaims(obc.Namespace).Update(obc)
		if err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("error updating phase: %v", err)
	}
	return obc, nil
}

func updateObjectBucketClaimPhase(c versioned.Interface, obc *v1alpha1.ObjectBucketClaim, phase v1alpha1.ObjectBucketClaimStatusPhase, retryInterval, retryTimeout time.Duration) (*v1alpha1.ObjectBucketClaim, error) {
	obc.Status.Phase = phase
	obc, err := updateClaim(c, obc, retryInterval, retryTimeout)
	if err != nil {
		return nil, err
	}
	return obc, nil
}

func updateObjectBucketPhase(c versioned.Interface, ob *v1alpha1.ObjectBucket, phase v1alpha1.ObjectBucketStatusPhase, retryInterval, retryTimeout time.Duration) (*v1alpha1.ObjectBucket, error) {
	ob.Status.Phase = phase
	err := wait.PollImmediate(retryInterval, retryTimeout, func() (done bool, err error) {
		ob, err = c.ObjectbucketV1alpha1().ObjectBuckets().Update(ob)
		if err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("error updating phase: %v", err)
	}
	return ob, nil
}
