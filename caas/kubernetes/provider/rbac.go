// Copyright 2019 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package provider

import (
	"fmt"

	"github.com/juju/errors"
	core "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/juju/juju/caas/specs"
)

func (k *kubernetesClient) getRBACLabels(appName string) map[string]string {
	return map[string]string{
		labelApplication: appName,
		labelModel:       k.namespace,
	}
}

func toK8sRules(rules []specs.PolicyRule) (out []rbacv1.PolicyRule) {
	for _, r := range rules {
		out = append(out, rbacv1.PolicyRule{
			Verbs:     r.Verbs,
			APIGroups: r.APIGroups,
			Resources: r.Resources,
		})
	}
	return out
}

type nameGetter interface {
	GetName() string
}

func getBindingName(sa nameGetter, cR nameGetter) string {
	return fmt.Sprintf("%s-%s", sa.GetName(), cR.GetName())
}

func (k *kubernetesClient) ensureServiceAccountForApp(
	appName string, caasSpec *specs.ServiceAccountSpec,
) (cleanups []func(), err error) {

	labels := k.getRBACLabels(appName)

	saSpec := &core.ServiceAccount{
		ObjectMeta: v1.ObjectMeta{
			Name:      caasSpec.GetName(),
			Namespace: k.namespace,
			Labels:    labels,
		},
		AutomountServiceAccountToken: caasSpec.AutomountServiceAccountToken,
	}
	// ensure service account;
	sa, saCleanups, err := k.ensureServiceAccount(saSpec)
	cleanups = append(cleanups, saCleanups...)
	if err != nil {
		return cleanups, errors.Trace(err)
	}

	// ensure roles.
	if len(caasSpec.Rules) > 0 {
		// create or update Role.
		r, rCleanups, err := k.ensureRole(&rbacv1.Role{
			ObjectMeta: v1.ObjectMeta{
				Name:      appName,
				Namespace: k.namespace,
				Labels:    labels,
			},
			Rules: toK8sRules(caasSpec.Rules),
		})
		cleanups = append(cleanups, rCleanups...)
		if err != nil {
			return cleanups, errors.Trace(err)
		}

		// ensure rolebindings for roles.
		_, rBCleanups, err := k.ensureRoleBinding(&rbacv1.RoleBinding{
			ObjectMeta: v1.ObjectMeta{
				Name:      appName,
				Namespace: k.namespace,
				Labels:    labels,
			},
			RoleRef: rbacv1.RoleRef{
				Name: r.GetName(),
				Kind: "Role",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      rbacv1.ServiceAccountKind,
					Name:      sa.GetName(),
					Namespace: sa.GetNamespace(),
				},
			},
		})
		cleanups = append(cleanups, rBCleanups...)
		if err != nil {
			return cleanups, errors.Trace(err)
		}
	}

	for _, clusterRoleName := range caasSpec.ClusterRoleNames {
		// check if ClusterRoles exist.
		cR, err := k.getClusterRole(clusterRoleName)
		if err != nil {
			return cleanups, errors.Trace(err)
		}
		_, cRBCleanups, err := k.ensureRoleBinding(&rbacv1.RoleBinding{
			ObjectMeta: v1.ObjectMeta{
				Name:      getBindingName(sa, cR),
				Namespace: k.namespace,
				Labels:    labels,
			},
			RoleRef: rbacv1.RoleRef{
				Name: cR.GetName(),
				Kind: "ClusterRole",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      rbacv1.ServiceAccountKind,
					Name:      sa.GetName(),
					Namespace: sa.GetNamespace(),
				},
			},
		})
		cleanups = append(cleanups, cRBCleanups...)
		if err != nil {
			return cleanups, errors.Trace(err)
		}
	}
	return cleanups, nil
}

func (k *kubernetesClient) deleteAllServiceAccountResources(appName string) error {
	if err := k.deleteRoleBindings(appName); err != nil {
		return errors.Trace(err)
	}
	if err := k.deleteClusterRoleBindings(appName); err != nil {
		return errors.Trace(err)
	}
	if err := k.deleteRoles(appName); err != nil {
		return errors.Trace(err)
	}
	if err := k.deleteClusterRoles(appName); err != nil {
		return errors.Trace(err)
	}
	if err := k.deleteServiceAccounts(appName); err != nil {
		return errors.Trace(err)
	}
	return nil
}

func (k *kubernetesClient) createServiceAccount(sa *core.ServiceAccount) (*core.ServiceAccount, error) {
	purifyResource(sa)
	out, err := k.client().CoreV1().ServiceAccounts(k.namespace).Create(sa)
	if k8serrors.IsAlreadyExists(err) {
		return nil, errors.AlreadyExistsf("service account %q", sa.GetName())
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) updateServiceAccount(sa *core.ServiceAccount) (*core.ServiceAccount, error) {
	out, err := k.client().CoreV1().ServiceAccounts(k.namespace).Update(sa)
	if k8serrors.IsNotFound(err) {
		return nil, errors.NotFoundf("service account %q", sa.GetName())
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) ensureServiceAccount(sa *core.ServiceAccount) (out *core.ServiceAccount, cleanups []func(), err error) {
	out, err = k.createServiceAccount(sa)
	if err == nil {
		logger.Debugf("service account %q created", out.GetName())
		cleanups = append(cleanups, func() { k.deleteServiceAccount(out.GetName(), out.GetUID()) })
		return out, cleanups, nil
	}
	if !errors.IsAlreadyExists(err) {
		return nil, cleanups, errors.Trace(err)
	}
	_, err = k.listServiceAccount(sa.GetLabels())
	if err != nil {
		if errors.IsNotFound(err) {
			// sa.Name is already used for an existing service account.
			return nil, cleanups, errors.AlreadyExistsf("service account %q", sa.GetName())
		}
		return nil, cleanups, errors.Trace(err)
	}
	out, err = k.updateServiceAccount(sa)
	logger.Debugf("updating service account %q", sa.GetName())
	return out, cleanups, errors.Trace(err)
}

func (k *kubernetesClient) getServiceAccount(name string) (*core.ServiceAccount, error) {
	out, err := k.client().CoreV1().ServiceAccounts(k.namespace).Get(name, v1.GetOptions{IncludeUninitialized: true})
	if k8serrors.IsNotFound(err) {
		return nil, errors.NotFoundf("service account %q", name)
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) deleteServiceAccount(name string, uid types.UID) error {
	err := k.client().CoreV1().ServiceAccounts(k.namespace).Delete(name, newPreconditionDeleteOptions(uid))
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return errors.Trace(err)
}

func (k *kubernetesClient) deleteServiceAccounts(appName string) error {
	err := k.client().CoreV1().ServiceAccounts(k.namespace).DeleteCollection(&v1.DeleteOptions{
		PropagationPolicy: &defaultPropagationPolicy,
	}, v1.ListOptions{
		LabelSelector:        labelsToSelector(k.getRBACLabels(appName)),
		IncludeUninitialized: true,
	})
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return errors.Trace(err)
}

func (k *kubernetesClient) listServiceAccount(labels map[string]string) ([]core.ServiceAccount, error) {
	listOps := v1.ListOptions{
		LabelSelector:        labelsToSelector(labels),
		IncludeUninitialized: true,
	}
	saList, err := k.client().CoreV1().ServiceAccounts(k.namespace).List(listOps)
	if err != nil {
		return nil, errors.Trace(err)
	}
	if len(saList.Items) == 0 {
		return nil, errors.NotFoundf("service account with labels %v", labels)
	}
	return saList.Items, nil
}

func (k *kubernetesClient) createRole(role *rbacv1.Role) (*rbacv1.Role, error) {
	purifyResource(role)
	out, err := k.client().RbacV1().Roles(k.namespace).Create(role)
	if k8serrors.IsAlreadyExists(err) {
		return nil, errors.AlreadyExistsf("role %q", role.GetName())
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) updateRole(role *rbacv1.Role) (*rbacv1.Role, error) {
	out, err := k.client().RbacV1().Roles(k.namespace).Update(role)
	if k8serrors.IsNotFound(err) {
		return nil, errors.NotFoundf("role %q", role.GetName())
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) ensureRole(role *rbacv1.Role) (out *rbacv1.Role, cleanups []func(), err error) {
	out, err = k.createRole(role)
	if err == nil {
		logger.Debugf("role %q created", out.GetName())
		cleanups = append(cleanups, func() { k.deleteRole(out.GetName(), out.GetUID()) })
		return out, cleanups, nil
	}
	if !errors.IsAlreadyExists(err) {
		return nil, cleanups, errors.Trace(err)
	}
	_, err = k.listRoles(role.GetLabels())
	if err != nil {
		if errors.IsNotFound(err) {
			// role.Name is already used for an existing role.
			return nil, cleanups, errors.AlreadyExistsf("role %q", role.GetName())
		}
		return nil, cleanups, errors.Trace(err)
	}
	out, err = k.updateRole(role)
	logger.Debugf("updating role %q", role.GetName())
	return out, cleanups, errors.Trace(err)
}

func (k *kubernetesClient) getRole(name string) (*rbacv1.Role, error) {
	out, err := k.client().RbacV1().Roles(k.namespace).Get(name, v1.GetOptions{IncludeUninitialized: true})
	if k8serrors.IsNotFound(err) {
		return nil, errors.NotFoundf("role %q", name)
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) deleteRole(name string, uid types.UID) error {
	err := k.client().RbacV1().Roles(k.namespace).Delete(name, newPreconditionDeleteOptions(uid))
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return errors.Trace(err)
}

func (k *kubernetesClient) deleteRoles(appName string) error {
	err := k.client().RbacV1().Roles(k.namespace).DeleteCollection(&v1.DeleteOptions{
		PropagationPolicy: &defaultPropagationPolicy,
	}, v1.ListOptions{
		LabelSelector:        labelsToSelector(k.getRBACLabels(appName)),
		IncludeUninitialized: true,
	})
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return errors.Trace(err)
}

func (k *kubernetesClient) listRoles(labels map[string]string) ([]rbacv1.Role, error) {
	listOps := v1.ListOptions{
		LabelSelector:        labelsToSelector(labels),
		IncludeUninitialized: true,
	}
	rList, err := k.client().RbacV1().Roles(k.namespace).List(listOps)
	if err != nil {
		return nil, errors.Trace(err)
	}
	if len(rList.Items) == 0 {
		return nil, errors.NotFoundf("role with labels %v", labels)
	}
	return rList.Items, nil
}

func (k *kubernetesClient) createClusterRole(cRole *rbacv1.ClusterRole) (*rbacv1.ClusterRole, error) {
	purifyResource(cRole)
	out, err := k.client().RbacV1().ClusterRoles().Create(cRole)
	if k8serrors.IsAlreadyExists(err) {
		return nil, errors.AlreadyExistsf("cluster role %q", cRole.GetName())
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) updateClusterRole(cRole *rbacv1.ClusterRole) (*rbacv1.ClusterRole, error) {
	out, err := k.client().RbacV1().ClusterRoles().Update(cRole)
	if k8serrors.IsNotFound(err) {
		return nil, errors.NotFoundf("cluster role %q", cRole.GetName())
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) ensureClusterRole(cRole *rbacv1.ClusterRole) (out *rbacv1.ClusterRole, cleanups []func(), err error) {
	out, err = k.createClusterRole(cRole)
	if err == nil {
		logger.Debugf("cluster role %q created", out.GetName())
		cleanups = append(cleanups, func() { k.deleteClusterRole(out.GetName(), out.GetUID()) })
		return out, cleanups, nil
	}
	if !errors.IsAlreadyExists(err) {
		return nil, cleanups, errors.Trace(err)
	}
	_, err = k.listClusterRoles(cRole.GetLabels())
	if err != nil {
		if errors.IsNotFound(err) {
			// cRole.Name is already used for an existing cluster role.
			return nil, cleanups, errors.AlreadyExistsf("cluster role %q", cRole.GetName())
		}
		return nil, cleanups, errors.Trace(err)
	}
	out, err = k.updateClusterRole(cRole)
	logger.Debugf("updating cluster role %q", cRole.GetName())
	return out, cleanups, errors.Trace(err)
}

func (k *kubernetesClient) getClusterRole(name string) (*rbacv1.ClusterRole, error) {
	out, err := k.client().RbacV1().ClusterRoles().Get(name, v1.GetOptions{IncludeUninitialized: true})
	if k8serrors.IsNotFound(err) {
		return nil, errors.NotFoundf("cluster role %q", name)
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) deleteClusterRole(name string, uid types.UID) error {
	err := k.client().RbacV1().ClusterRoles().Delete(name, newPreconditionDeleteOptions(uid))
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return errors.Trace(err)
}

func (k *kubernetesClient) deleteClusterRoles(appName string) error {
	err := k.client().RbacV1().ClusterRoles().DeleteCollection(&v1.DeleteOptions{
		PropagationPolicy: &defaultPropagationPolicy,
	}, v1.ListOptions{
		LabelSelector:        labelsToSelector(k.getRBACLabels(appName)),
		IncludeUninitialized: true,
	})
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return errors.Trace(err)
}

func (k *kubernetesClient) listClusterRoles(labels map[string]string) ([]rbacv1.ClusterRole, error) {
	listOps := v1.ListOptions{
		LabelSelector:        labelsToSelector(labels),
		IncludeUninitialized: true,
	}
	cRList, err := k.client().RbacV1().ClusterRoles().List(listOps)
	if err != nil {
		return nil, errors.Trace(err)
	}
	if len(cRList.Items) == 0 {
		return nil, errors.NotFoundf("cluster role with labels %v", labels)
	}
	return cRList.Items, nil
}

func (k *kubernetesClient) createRoleBinding(rb *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error) {
	purifyResource(rb)
	out, err := k.client().RbacV1().RoleBindings(k.namespace).Create(rb)
	if k8serrors.IsAlreadyExists(err) {
		return nil, errors.AlreadyExistsf("role binding %q", rb.GetName())
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) updateRoleBinding(rb *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error) {
	out, err := k.client().RbacV1().RoleBindings(k.namespace).Update(rb)
	if k8serrors.IsNotFound(err) {
		return nil, errors.NotFoundf("role binding %q", rb.GetName())
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) ensureRoleBinding(rb *rbacv1.RoleBinding) (out *rbacv1.RoleBinding, cleanups []func(), err error) {
	isFirstDeploy := false
	// RoleRef is immutable, so delete first then re-create.
	rbs, err := k.listRoleBindings(rb.GetLabels())
	if errors.IsNotFound(err) {
		isFirstDeploy = true
	} else if err != nil {
		return nil, cleanups, errors.Trace(err)
	}

	for _, v := range rbs {
		if v.GetName() == rb.GetName() {
			if err := k.deleteRoleBinding(v.GetName(), v.GetUID()); err != nil {
				return nil, cleanups, errors.Trace(err)
			}
			logger.Debugf("role binding %q deleted", v.GetName())
			break
		}
	}
	out, err = k.createRoleBinding(rb)
	if err != nil {
		return nil, cleanups, errors.Trace(err)
	}
	if isFirstDeploy {
		// only do cleanup for the first time, don't do this for existing deployments.
		cleanups = append(cleanups, func() { k.deleteRoleBinding(out.GetName(), out.GetUID()) })
	}
	logger.Debugf("role binding %q created", rb.GetName())
	return out, cleanups, nil
}

func (k *kubernetesClient) getRoleBinding(name string) (*rbacv1.RoleBinding, error) {
	out, err := k.client().RbacV1().RoleBindings(k.namespace).Get(name, v1.GetOptions{IncludeUninitialized: true})
	if k8serrors.IsNotFound(err) {
		return nil, errors.NotFoundf("role binding %q", name)
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) deleteRoleBinding(name string, uid types.UID) error {
	err := k.client().RbacV1().RoleBindings(k.namespace).Delete(name, newPreconditionDeleteOptions(uid))
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return errors.Trace(err)
}

func (k *kubernetesClient) deleteRoleBindings(appName string) error {
	err := k.client().RbacV1().RoleBindings(k.namespace).DeleteCollection(&v1.DeleteOptions{
		PropagationPolicy: &defaultPropagationPolicy,
	}, v1.ListOptions{
		LabelSelector:        labelsToSelector(k.getRBACLabels(appName)),
		IncludeUninitialized: true,
	})
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return errors.Trace(err)
}

func (k *kubernetesClient) listRoleBindings(labels map[string]string) ([]rbacv1.RoleBinding, error) {
	listOps := v1.ListOptions{
		LabelSelector:        labelsToSelector(labels),
		IncludeUninitialized: true,
	}
	rBList, err := k.client().RbacV1().RoleBindings(k.namespace).List(listOps)
	if err != nil {
		return nil, errors.Trace(err)
	}
	if len(rBList.Items) == 0 {
		return nil, errors.NotFoundf("role binding with labels %v", labels)
	}
	return rBList.Items, nil
}

func (k *kubernetesClient) createClusterRoleBinding(crb *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRoleBinding, error) {
	purifyResource(crb)
	out, err := k.client().RbacV1().ClusterRoleBindings().Create(crb)
	if k8serrors.IsAlreadyExists(err) {
		return nil, errors.AlreadyExistsf("cluster role binding %q", crb.GetName())
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) updateClusterRoleBinding(crb *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRoleBinding, error) {
	out, err := k.client().RbacV1().ClusterRoleBindings().Update(crb)
	if k8serrors.IsNotFound(err) {
		return nil, errors.NotFoundf("cluster role binding %q", crb.GetName())
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) ensureClusterRoleBinding(crb *rbacv1.ClusterRoleBinding) (out *rbacv1.ClusterRoleBinding, cleanups []func(), err error) {
	isFirstDeploy := false
	// RoleRef is immutable, so delete first then re-create.
	crbs, err := k.listClusterRoleBindings(crb.GetLabels())
	if errors.IsNotFound(err) {
		isFirstDeploy = true
	} else if err != nil {
		return nil, cleanups, errors.Trace(err)
	}

	for _, v := range crbs {
		if err := k.deleteClusterRoleBinding(v.GetName(), v.GetUID()); err != nil {
			return nil, cleanups, errors.Trace(err)
		}
		logger.Debugf("cluster role binding %q deleted", v.GetName())
	}
	out, err = k.createClusterRoleBinding(crb)
	if err != nil {
		return nil, cleanups, errors.Trace(err)
	}
	if isFirstDeploy {
		cleanups = append(cleanups, func() { k.deleteClusterRoleBinding(out.GetName(), out.GetUID()) })
	}
	logger.Debugf("cluster role binding %q created", crb.GetName())
	return out, cleanups, nil
}

func (k *kubernetesClient) getClusterRoleBinding(name string) (*rbacv1.ClusterRoleBinding, error) {
	out, err := k.client().RbacV1().ClusterRoleBindings().Get(name, v1.GetOptions{IncludeUninitialized: true})
	if k8serrors.IsNotFound(err) {
		return nil, errors.NotFoundf("cluster role binding %q", name)
	}
	return out, errors.Trace(err)
}

func (k *kubernetesClient) deleteClusterRoleBinding(name string, uid types.UID) error {
	err := k.client().RbacV1().ClusterRoleBindings().Delete(name, newPreconditionDeleteOptions(uid))
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return errors.Trace(err)
}

func (k *kubernetesClient) deleteClusterRoleBindings(appName string) error {
	err := k.client().RbacV1().ClusterRoleBindings().DeleteCollection(&v1.DeleteOptions{
		PropagationPolicy: &defaultPropagationPolicy,
	}, v1.ListOptions{
		LabelSelector:        labelsToSelector(k.getRBACLabels(appName)),
		IncludeUninitialized: true,
	})
	if k8serrors.IsNotFound(err) {
		return nil
	}
	return errors.Trace(err)
}

func (k *kubernetesClient) listClusterRoleBindings(labels map[string]string) ([]rbacv1.ClusterRoleBinding, error) {
	listOps := v1.ListOptions{
		LabelSelector:        labelsToSelector(labels),
		IncludeUninitialized: true,
	}
	cRBList, err := k.client().RbacV1().ClusterRoleBindings().List(listOps)
	if err != nil {
		return nil, errors.Trace(err)
	}
	if len(cRBList.Items) == 0 {
		return nil, errors.NotFoundf("cluster role binding with labels %v", labels)
	}
	return cRBList.Items, nil
}
