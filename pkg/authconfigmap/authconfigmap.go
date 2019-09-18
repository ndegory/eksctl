// Package authconfigmap allows manipulation of the EKS auth ConfigMap (aws-auth),
// which maps IAM entities to Kubernetes groups.
//
// See for more information:
// - https://docs.aws.amazon.com/eks/latest/userguide/add-user-role.html
// - https://github.com/kubernetes-sigs/aws-iam-authenticator/blob/master/README.md#full-configuration-format
package authconfigmap

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws/awsutil"
	"github.com/kris-nova/logger"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/yaml"

	api "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/iam"
)

const (
	// ObjectName is the Kubernetes resource name of the auth ConfigMap
	ObjectName = "aws-auth"
	// ObjectNamespace is the namespace the object can be found
	ObjectNamespace = metav1.NamespaceSystem

	rolesData = "mapRoles"

	usersData = "mapUsers"

	accountsData = "mapAccounts"

	// GroupMasters is the admin group which is also automatically
	// granted to the IAM role that creates the cluster.
	GroupMasters = "system:masters"

	// RoleNodeGroupUsername is the default username for a nodegroup
	// role mapping.
	RoleNodeGroupUsername = "system:node:{{EC2PrivateDNSName}}"
)

// RoleNodeGroupGroups are the groups to allow roles to interact
// with the cluster, required for the instance role ARNs of nodegroups.
var RoleNodeGroupGroups = []string{"system:bootstrappers", "system:nodes"}

// AuthConfigMap allows modifying the auth ConfigMap.
type AuthConfigMap struct {
	client v1.ConfigMapInterface
	cm     *corev1.ConfigMap
}

// New creates an AuthConfigMap instance that manipulates
// a ConfigMap. If it is nil, one is created.
func New(client v1.ConfigMapInterface, cm *corev1.ConfigMap) *AuthConfigMap {
	if cm == nil {
		cm = &corev1.ConfigMap{
			ObjectMeta: ObjectMeta(),
			Data:       map[string]string{},
		}
	}
	if cm.Data == nil {
		cm.ObjectMeta = ObjectMeta()
		cm.Data = map[string]string{}
	}
	return &AuthConfigMap{client: client, cm: cm}
}

// NewFromClientSet fetches the auth ConfigMap.
func NewFromClientSet(clientSet kubernetes.Interface) (*AuthConfigMap, error) {
	client := clientSet.CoreV1().ConfigMaps(ObjectNamespace)

	cm, err := client.Get(ObjectName, metav1.GetOptions{})
	// It is fine for the configmap not to exist. Any other error is fatal.
	if err != nil && !kerr.IsNotFound(err) {
		return nil, errors.Wrapf(err, "getting auth ConfigMap")
	}
	logger.Debug("aws-auth = %s", awsutil.Prettify(cm))
	return New(client, cm), nil
}

// AddAccount appends an IAM account to the `mapAccounts` entry
// in the Configmap. It also deduplicates.
func (a *AuthConfigMap) AddAccount(account string) error {
	accounts, err := a.accounts()
	if err != nil {
		return err
	}
	// Distinct and sorted account numbers
	accounts = append(accounts, account)
	accounts = sets.NewString(accounts...).List()
	logger.Info("adding account %q to auth ConfigMap", account)
	return a.setAccounts(accounts)
}

// RemoveAccount removes the given IAM account entry in mapAccounts.
func (a *AuthConfigMap) RemoveAccount(account string) error {
	accounts, err := a.accounts()
	if err != nil {
		return err
	}

	var newaccounts []string
	found := false
	for _, acc := range accounts {
		if acc == account {
			found = true
			continue
		}
		newaccounts = append(newaccounts, acc)
	}
	if !found {
		return fmt.Errorf("account %q not found in auth ConfigMap", account)
	}
	logger.Info("removing account %q from auth ConfigMap", account)
	return a.setAccounts(newaccounts)
}

func (a *AuthConfigMap) accounts() ([]string, error) {
	var accounts []string
	if err := yaml.Unmarshal([]byte(a.cm.Data[accountsData]), &accounts); err != nil {
		return nil, errors.Wrap(err, "unmarshalling mapAccounts")
	}
	return accounts, nil
}

func (a *AuthConfigMap) setAccounts(accounts []string) error {
	bs, err := yaml.Marshal(accounts)
	if err != nil {
		return errors.Wrap(err, "marshalling mapAccounts")
	}
	a.cm.Data[accountsData] = string(bs)
	return nil
}

// AddIdentity maps an IAM role or user ARN to a k8s group dynamically. It modifies the
// role or user with given groups. If you are calling
// this as part of node creation you should use DefaultNodeGroups.
func (a *AuthConfigMap) AddIdentity(identity iam.Identity) error {
	identities, err := a.Identities()
	if err != nil {
		return err
	}

	// Should the caller do this check or should this happen here?
	if err = identity.Valid(); err != nil {
		return err
	}

	identities = append(identities, identity)

	arn, _ := identity.ARN() // The Valid() call above makes sure this cannot error
	logger.Info("adding identity %q to auth ConfigMap", arn.String())
	return a.setIdentities(identities)
}

// RemoveIdentity removes an identity. If `all` is false it will only
// remove the first it encounters and return an error if it cannot
// find it.
// If `all` is true it will remove all of them and not return an
// error if it cannot be found.
func (a *AuthConfigMap) RemoveIdentity(arn iam.ARN, all bool) error {
	identities, err := a.Identities()
	if err != nil {
		return err
	}

	newidentities := []iam.Identity{}
	for i, identity := range identities {
		_arn, err := identity.ARN()
		if err != nil {
			return err
		}
		if _arn.String() == arn.String() {
			logger.Info("removing identity %q from auth ConfigMap (username = %q, groups = %q)", arn.String(), identity.Username, identity.Groups)
			if !all {
				identities = append(identities[:i], identities[i+1:]...)
				return a.setIdentities(identities)
			}
		} else if all {
			newidentities = append(newidentities, identity)
		}
	}
	if !all {
		return fmt.Errorf("instance identity ARN %q not found in auth ConfigMap", arn.String())
	}
	return a.setIdentities(newidentities)
}

// Identities returns a list of iam users and roles that are currently in the (cached) configmap.
func (a *AuthConfigMap) Identities() ([]iam.Identity, error) {
	var roles, users []iam.Identity
	if err := yaml.Unmarshal([]byte(a.cm.Data[rolesData]), &roles); err != nil {
		return nil, errors.Wrapf(err, "unmarshalling %q", rolesData)
	}

	if err := yaml.Unmarshal([]byte(a.cm.Data[usersData]), &users); err != nil {
		return nil, errors.Wrapf(err, "unmarshalling %q", usersData)
	}
	return append(roles, users...), nil
}

func (a *AuthConfigMap) setIdentities(identities []iam.Identity) error {
	// Determine which are users and which are roles
	users, roles := []iam.Identity{}, []iam.Identity{}
	for _, identity := range identities {
		arn, err := identity.ARN()
		if err != nil {
			return err
		}

		switch {
		case arn.Role():
			roles = append(roles, identity)
		case arn.User():
			users = append(users, identity)
		default:
			return errors.Errorf("cannot determine if %q refers to a user or role during setIdentities preprocessing", arn.Resource)
		}
	}

	// Update the corresponding keys
	_roles, err := yaml.Marshal(roles)
	if err != nil {
		return errors.Wrapf(err, "marshalling %q", rolesData)
	}
	a.cm.Data[rolesData] = string(_roles)

	_users, err := yaml.Marshal(users)
	if err != nil {
		return errors.Wrapf(err, "marshalling %q", usersData)
	}
	a.cm.Data[usersData] = string(_users)

	return nil
}

// Save persists the ConfigMap to the cluster. It determines
// whether to create or update by looking at the ConfigMap's UID.
func (a *AuthConfigMap) Save() (err error) {
	if a.cm.UID == "" {
		a.cm, err = a.client.Create(a.cm)
		return err
	}

	a.cm, err = a.client.Update(a.cm)
	return err
}

// ObjectMeta constructs metadata for the ConfigMap.
func ObjectMeta() metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      ObjectName,
		Namespace: ObjectNamespace,
	}
}

// AddNodeGroup creates or adds a nodegroup IAM role in the auth
// ConfigMap for the given nodegroup.
func AddNodeGroup(clientSet kubernetes.Interface, ng *api.NodeGroup) error {
	acm, err := NewFromClientSet(clientSet)
	if err != nil {
		return err
	}

	arn, err := iam.Parse(ng.IAM.InstanceRoleARN)
	if err != nil {
		return errors.Wrap(err, "parsing nodegroup instance role ARN")
	}

	identity, err := iam.NewIdentity(arn, RoleNodeGroupUsername, RoleNodeGroupGroups)
	if err != nil {
		return err
	}

	if err := acm.AddIdentity(*identity); err != nil {
		return errors.Wrap(err, "adding nodegroup to auth ConfigMap")
	}
	if err := acm.Save(); err != nil {
		return errors.Wrap(err, "saving auth ConfigMap")
	}
	logger.Debug("saved auth ConfigMap for %q", ng.Name)
	return nil
}

// RemoveNodeGroup removes a nodegroup from the ConfigMap and
// does a client update.
func RemoveNodeGroup(clientSet kubernetes.Interface, ng *api.NodeGroup) error {
	arn, err := iam.Parse(ng.IAM.InstanceRoleARN)
	if err != nil {
		return errors.Wrap(err, "nodegroup instance role ARN is invalid")
	}
	acm, err := NewFromClientSet(clientSet)
	if err != nil {
		return err
	}
	if err := acm.RemoveIdentity(arn, false); err != nil {
		return errors.Wrap(err, "removing nodegroup from auth ConfigMap")
	}
	if err := acm.Save(); err != nil {
		return errors.Wrap(err, "updating auth ConfigMap after removing role")
	}
	logger.Debug("updated auth ConfigMap for %s", ng.Name)
	return nil
}
