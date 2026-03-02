package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// HealthHandler returns an http.HandlerFunc that returns platform health
// including deployments, statefulsets, and pods in the hosting namespace.
func HealthHandler(clientset kubernetes.Interface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		resp := map[string]interface{}{"status": "ok"}

		if clientset == nil {
			resp["status"] = "degraded"
			writeJSON(w, http.StatusServiceUnavailable, resp)
			return
		}

		ns := hostingNamespace

		// Deployments
		deps, err := clientset.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
		var depList []map[string]interface{}
		if err == nil {
			for _, d := range deps.Items {
				depList = append(depList, map[string]interface{}{
					"name":      d.Name,
					"ready":     d.Status.ReadyReplicas,
					"desired":   *d.Spec.Replicas,
					"available": d.Status.AvailableReplicas > 0,
				})
			}
		}
		resp["deployments"] = depList

		// StatefulSets
		ssets, err := clientset.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
		var ssList []map[string]interface{}
		if err == nil {
			for _, s := range ssets.Items {
				ssList = append(ssList, map[string]interface{}{
					"name":      s.Name,
					"ready":     s.Status.ReadyReplicas,
					"desired":   *s.Spec.Replicas,
					"available": s.Status.ReadyReplicas > 0,
				})
			}
		}
		resp["statefulSets"] = ssList

		// Pods
		pods, err := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		var podList []map[string]interface{}
		if err == nil {
			for _, p := range pods.Items {
				restarts := int32(0)
				ready := true
				var containers []map[string]interface{}
				for _, cs := range p.Status.ContainerStatuses {
					restarts += cs.RestartCount
					if !cs.Ready {
						ready = false
					}
					state := "running"
					if cs.State.Waiting != nil {
						state = cs.State.Waiting.Reason
					} else if cs.State.Terminated != nil {
						state = "terminated"
					}
					containers = append(containers, map[string]interface{}{
						"name": cs.Name, "state": state, "restartCount": cs.RestartCount,
					})
				}
				podList = append(podList, map[string]interface{}{
					"name": p.Name, "phase": string(p.Status.Phase),
					"restarts": restarts, "ready": ready, "containers": containers,
				})
			}
		}
		resp["pods"] = podList

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
