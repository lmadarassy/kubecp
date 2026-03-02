/*
Copyright 2026.

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

package controller

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hostingv1alpha1 "github.com/hosting-panel/hosting-operator/api/v1alpha1"
)

const (
	databaseFinalizer = "hosting.panel/database-cleanup"
	galeraHost        = "hosting-panel-mariadb-galera.hosting-system.svc.cluster.local"
	galeraPort        = 3306
)

// DatabaseReconciler reconciles a Database object
type DatabaseReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	MariaDBRoot string // root password for MariaDB
}

// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=databases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=databases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=hosting.hosting.panel,resources=databases/finalizers,verbs=update

// Reconcile moves the cluster state toward the desired state for a Database resource.
func (r *DatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	db := &hostingv1alpha1.Database{}
	if err := r.Get(ctx, req.NamespacedName, db); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Database resource not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !db.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, db)
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(db, databaseFinalizer) {
		controllerutil.AddFinalizer(db, databaseFinalizer)
		if err := r.Update(ctx, db); err != nil {
			return ctrl.Result{}, err
		}
		// Return immediately — the Update triggers a new reconcile with the fresh object,
		// preventing "object has been modified" conflicts in subsequent status updates.
		return ctrl.Result{}, nil
	}

	// Set phase to Creating if empty or Pending
	if db.Status.Phase == "" || db.Status.Phase == "Pending" {
		db.Status.Phase = "Creating"
		if err := r.Status().Update(ctx, db); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Skip re-provisioning if already Ready
	if db.Status.Phase == "Ready" && db.Status.Password != "" {
		return ctrl.Result{}, nil
	}

	// Skip if already provisioning (password set but not yet Ready)
	if db.Status.Phase == "Creating" && db.Status.Password != "" {
		return r.updateReadyStatus(ctx, db)
	}

	// Provision database and user in Galera cluster
	if err := r.provisionDatabase(ctx, db); err != nil {
		return r.setErrorStatus(ctx, db, "ProvisionFailed", err)
	}

	// Update status to Ready
	return r.updateReadyStatus(ctx, db)
}

// reconcileDelete handles Database deletion — drops DB and user, removes finalizer.
func (r *DatabaseReconciler) reconcileDelete(ctx context.Context, db *hostingv1alpha1.Database) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	db.Status.Phase = "Terminating"
	_ = r.Status().Update(ctx, db)

	dbName := db.Spec.Name
	username := fmt.Sprintf("usr_%s", dbName)
	log.Info("Dropping database and revoking user", "database", dbName, "user", username)

	dsn := fmt.Sprintf("root:%s@tcp(%s:%d)/", r.MariaDBRoot, galeraHost, galeraPort)
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Error(err, "Failed to connect to MariaDB for cleanup, removing finalizer anyway")
	} else {
		defer conn.Close()
		if err := conn.PingContext(ctx); err != nil {
			log.Error(err, "MariaDB ping failed during cleanup")
		} else {
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName)); err != nil {
				log.Error(err, "Failed to drop database", "database", dbName)
			}
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("DROP USER IF EXISTS '%s'@'%%'", username)); err != nil {
				log.Error(err, "Failed to drop user", "user", username)
			}
		}
	}

	controllerutil.RemoveFinalizer(db, databaseFinalizer)
	if err := r.Update(ctx, db); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// provisionDatabase creates the database and dedicated user in the Galera cluster.
func (r *DatabaseReconciler) provisionDatabase(ctx context.Context, db *hostingv1alpha1.Database) error {
	log := logf.FromContext(ctx)

	dsn := fmt.Sprintf("root:%s@tcp(%s:%d)/", r.MariaDBRoot, galeraHost, galeraPort)
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("failed to connect to MariaDB: %w", err)
	}
	defer conn.Close()

	if err := conn.PingContext(ctx); err != nil {
		return fmt.Errorf("MariaDB ping failed: %w", err)
	}

	dbName := db.Spec.Name
	charset := db.Spec.Charset
	if charset == "" {
		charset = "utf8mb4"
	}
	collation := db.Spec.Collation
	if collation == "" {
		collation = "utf8mb4_unicode_ci"
	}

	username := fmt.Sprintf("usr_%s", dbName)

	// Generate password if not already set in status
	password := db.Status.Password
	if password == "" {
		password, err = generatePassword(16)
		if err != nil {
			return fmt.Errorf("failed to generate password: %w", err)
		}
		// Persist password in status IMMEDIATELY to prevent race condition:
		// If Reconcile retries before updateReadyStatus, the password would be
		// re-generated causing a mismatch between K8s status and MariaDB.
		db.Status.Password = password
		if err := r.Status().Update(ctx, db); err != nil {
			return fmt.Errorf("failed to persist password in status: %w", err)
		}
	}

	log.Info("Provisioning database", "database", dbName, "user", username)

	// Create database
	_, err = conn.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET %s COLLATE %s", dbName, charset, collation))
	if err != nil {
		return fmt.Errorf("failed to create database: %w", err)
	}

	// Create user and grant privileges
	_, err = conn.ExecContext(ctx, fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s'", username, password))
	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}

	// Always update password (handles password change case)
	_, err = conn.ExecContext(ctx, fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED BY '%s'", username, password))
	if err != nil {
		return fmt.Errorf("failed to update user password: %w", err)
	}

	_, err = conn.ExecContext(ctx, fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%%'", dbName, username))
	if err != nil {
		return fmt.Errorf("failed to grant privileges: %w", err)
	}

	_, err = conn.ExecContext(ctx, "FLUSH PRIVILEGES")
	if err != nil {
		return fmt.Errorf("failed to flush privileges: %w", err)
	}

	// Store password in status for later retrieval
	db.Status.Password = password

	return nil
}

// updateReadyStatus sets the Database status to Ready.
func (r *DatabaseReconciler) updateReadyStatus(ctx context.Context, db *hostingv1alpha1.Database) (ctrl.Result, error) {
	username := fmt.Sprintf("usr_%s", db.Spec.Name)

	db.Status.Phase = "Ready"
	db.Status.Host = galeraHost
	db.Status.Port = galeraPort
	db.Status.DatabaseName = db.Spec.Name
	db.Status.Username = username
	// Password is already set by provisionDatabase

	meta.SetStatusCondition(&db.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Provisioned",
		Message:            "Database and user created successfully",
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, db); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// generatePassword creates a random hex password of the given byte length.
func generatePassword(byteLen int) (string, error) {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// setErrorStatus updates the Database status to Error.
func (r *DatabaseReconciler) setErrorStatus(ctx context.Context, db *hostingv1alpha1.Database, reason string, err error) (ctrl.Result, error) {
	db.Status.Phase = "Error"
	meta.SetStatusCondition(&db.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            err.Error(),
		LastTransitionTime: metav1.Now(),
	})
	_ = r.Status().Update(ctx, db)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *DatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hostingv1alpha1.Database{}).
		Named("database").
		Complete(r)
}
