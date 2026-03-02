package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hostingv1alpha1 "github.com/hosting-panel/hosting-operator/api/v1alpha1"
)

const dovecotPasswdSecretName = "dovecot-passwd"

// Deprecated: Dovecot now authenticates via Keycloak checkpassword. Kept for backward compat.
func (r *EmailAccountReconciler) reconcileDovecotPasswd(ctx context.Context, namespace string) error {
	log := logf.FromContext(ctx)

	// List all EmailAccount resources in the namespace
	emailList := &hostingv1alpha1.EmailAccountList{}
	if err := r.List(ctx, emailList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list EmailAccounts: %w", err)
	}

	// Build passwd-file content
	var lines []string
	for _, email := range emailList.Items {
		if email.DeletionTimestamp != nil {
			continue
		}
		if email.Spec.PasswordHash == "" {
			continue
		}
		address := email.Spec.Address
		domain := email.Spec.Domain
		accountName := address
		if idx := strings.Index(address, "@"); idx > 0 {
			accountName = address[:idx]
		}
		// Dovecot passwd-file format:
		// user:{scheme}hash:uid:gid::home::userdb_mail=maildir:path
		line := fmt.Sprintf("%s:%s:5000:5000::/var/mail/vhosts/%s/%s::userdb_mail=maildir:/var/mail/vhosts/%s/%s/Maildir",
			address, email.Spec.PasswordHash, domain, accountName, domain, accountName)
		lines = append(lines, line)
	}

	passwdContent := strings.Join(lines, "\n")
	if len(lines) > 0 {
		passwdContent += "\n"
	}

	// Create or update the Secret
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: dovecotPasswdSecretName, Namespace: namespace}, secret)
	if errors.IsNotFound(err) {
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      dovecotPasswdSecretName,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/name":       "dovecot-passwd",
					"app.kubernetes.io/component":  "mail",
					"app.kubernetes.io/managed-by": "hosting-operator",
				},
			},
			StringData: map[string]string{
				"passwd": passwdContent,
			},
		}
		log.Info("Creating Dovecot passwd Secret", "accounts", len(lines))
		return r.Create(ctx, secret)
	}
	if err != nil {
		return fmt.Errorf("failed to get dovecot-passwd Secret: %w", err)
	}

	// Update existing Secret only if content changed
	currentContent := string(secret.Data["passwd"])
	if currentContent != passwdContent {
		secret.StringData = map[string]string{
			"passwd": passwdContent,
		}
		log.Info("Updating Dovecot passwd Secret", "accounts", len(lines))
		return r.Update(ctx, secret)
	}

	return nil
}
