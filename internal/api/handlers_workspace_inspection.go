package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// defaultLogTail is the number of trailing log lines served when the client
// does not ask for a specific amount.
const defaultLogTail = 200

// maxEventsReturned caps the event history served per workspace.
const maxEventsReturned = 200

// ------------------------------------------------------------------- logs --

// handleWorkspaceLogs streams the logs of a workspace pod as chunked
// text/plain. Query parameters:
//
//	container  container name (default "workspace")
//	tail       number of trailing lines, "all" or <= 0 for everything
//	           (default 200)
//	follow     "true"/"1" to keep the stream open (live tail)
//	previous   "true"/"1" for the previous (restarted) container instance
func (s *Server) handleWorkspaceLogs(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.getWorkspace(w, r)
	if !ok {
		return
	}
	if s.kube == nil {
		writeErr(w, http.StatusNotImplemented, "log streaming is not available")
		return
	}
	podName := ws.Status.PodName
	if podName == "" {
		podName = ws.Name
	}
	var pod corev1.Pod
	if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: ws.Namespace, Name: podName}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			writeErr(w, http.StatusConflict, "workspace is not running — start it to stream logs")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	q := r.URL.Query()
	container := q.Get("container")
	if container == "" {
		container = "workspace"
	}
	if names := podContainerNames(&pod); !contains(names, container) {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("container %q not found (available: %s)", container, strings.Join(names, ", ")))
		return
	}

	tail := int64(defaultLogTail)
	if v := q.Get("tail"); v != "" {
		if v == "all" {
			tail = 0
		} else if n, err := strconv.ParseInt(v, 10, 64); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid tail parameter (number or \"all\")")
			return
		} else {
			tail = n
		}
	}
	follow := boolParam(q, "follow")
	previous := boolParam(q, "previous")

	stream, err := s.kube.StreamPodLogs(r.Context(), ws.Namespace, podName, container, tail, follow, previous)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "opening log stream: "+err.Error())
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	buf := make([]byte, 32*1024)
	for {
		n, rerr := stream.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // client went away
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				// A followed stream ends when the pod goes away; mark it
				// so tailing UIs/CLIs can tell stop from disconnect.
				if follow {
					fmt.Fprintln(w, "\n[devws] log stream ended")
					if flusher != nil {
						flusher.Flush()
					}
				}
			} else {
				fmt.Fprintf(w, "\n[devws] log stream error: %v\n", rerr)
			}
			return
		}
	}
}

func boolParam(q url.Values, name string) bool {
	v := strings.ToLower(q.Get(name))
	return v == "true" || v == "1" || v == "yes"
}

func podContainerNames(pod *corev1.Pod) []string {
	names := make([]string, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	for _, c := range pod.Spec.InitContainers {
		names = append(names, c.Name)
	}
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	return names
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------- events --

// eventView is a kubernetes event relevant to a workspace.
type eventView struct {
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Object    string `json:"object"`
	Message   string `json:"message"`
	Count     int32  `json:"count"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
}

// handleWorkspaceEvents returns the kubernetes events recorded for a
// workspace's objects: the Workspace CR, its pod, PVC and ssh secret.
func (s *Server) handleWorkspaceEvents(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.getWorkspace(w, r)
	if !ok {
		return
	}
	if s.kube == nil {
		writeErr(w, http.StatusNotImplemented, "event listing is not available")
		return
	}
	events, err := s.kube.ListEvents(r.Context(), ws.Namespace)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "listing events: "+err.Error())
		return
	}

	related := map[string]bool{
		ws.Name:           true, // workspace CR and pod share the name
		ws.Name + "-home": true, // pvc
		ws.Name + "-ssh":  true, // authorized_keys secret
	}
	views := []eventView{}
	for i := range events {
		e := &events[i]
		if !related[e.InvolvedObject.Name] {
			continue
		}
		last := e.LastTimestamp
		if last.IsZero() {
			last = metav1.Time{Time: e.EventTime.Time}
		}
		first := e.FirstTimestamp
		if first.IsZero() {
			first = last
		}
		views = append(views, eventView{
			Type:      e.Type,
			Reason:    e.Reason,
			Object:    strings.ToLower(e.InvolvedObject.Kind) + "/" + e.InvolvedObject.Name,
			Message:   e.Message,
			Count:     e.Count,
			FirstSeen: first.UTC().Format(time.RFC3339),
			LastSeen:  last.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].LastSeen < views[j].LastSeen })
	if len(views) > maxEventsReturned {
		views = views[len(views)-maxEventsReturned:]
	}
	writeJSON(w, http.StatusOK, views)
}
