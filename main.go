package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

//go:embed static/index.html
var pageFS embed.FS

var pageTemplates = template.Must(template.New("index.html").
	Funcs(template.FuncMap{
		"formatTime": func(value *time.Time) string {
			if value == nil {
				return "—"
			}
			return value.Local().Format("Jan 2, 15:04")
		},
	}).
	ParseFS(pageFS, "static/index.html"))

type Server struct {
	client    kubernetes.Interface
	namespace string
	endpoint  string
	maxJobs   int
}

type JobRow struct {
	Name            string     `json:"name"`
	Namespace       string     `json:"namespace"`
	Status          string     `json:"status"`
	Start           *time.Time `json:"start,omitempty"`
	Completion      *time.Time `json:"completion,omitempty"`
	DurationSeconds int64      `json:"durationSeconds"`
	Succeeded       int32      `json:"succeeded"`
	Failed          int32      `json:"failed"`
	Active          int32      `json:"active"`
}

type CronJobRow struct {
	Name         string     `json:"name"`
	Namespace    string     `json:"namespace"`
	Schedule     string     `json:"schedule"`
	Suspended    bool       `json:"suspended"`
	Active       int        `json:"active"`
	LastSchedule *time.Time `json:"lastSchedule,omitempty"`
	Jobs         []JobRow   `json:"jobs"`
}

type DashboardView struct {
	CronJobs       []CronJobRow
	ActiveCronJobs int
	Suspended      int
	JobCount       int
}

func main() {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatal(err)
	}

	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		log.Fatal("NAMESPACE must be configured")
	}

	authEnabled := false
	if raw := os.Getenv("OAUTH2_PROXY_ENABLED"); raw != "" {
		authEnabled, err = strconv.ParseBool(raw)
		if err != nil {
			log.Fatalf("invalid OAUTH2_PROXY_ENABLED=%q: must be a boolean", raw)
		}
	}

	publicURL := os.Getenv("PUBLIC_URL")
	if publicURL == "" {
		if authEnabled {
			log.Fatal("PUBLIC_URL must be configured when OAUTH2_PROXY_ENABLED is true")
		}
		publicURL = "http://localhost:8080"
	}

	access, err := newAccessControl(
		os.Getenv("OAUTH2_PROXY_AUTH_URL"),
		os.Getenv("AUTH_EMAIL_DOMAIN"),
		publicURL,
	)
	if err != nil {
		log.Fatal(err)
	}
	access.enabled = authEnabled

	endpoint := os.Getenv("ENDPOINT")
	if endpoint == "" {
		endpoint = "/"
	}

	maxJobs := 10

	if raw := os.Getenv("MAX_JOBS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			log.Fatalf("invalid MAX_JOBS=%q", raw)
		}
		maxJobs = n
	}

	server := &Server{
		client:    client,
		namespace: namespace,
		endpoint:  endpoint,
		maxJobs:   maxJobs,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/", server.home)
	mux.HandleFunc("/partials/dashboard", server.dashboard)
	mux.HandleFunc("/api/config", server.config)
	mux.HandleFunc("/api/cronjobs", server.cronjobs)
	mux.HandleFunc("/api/cronjobs/", server.cronjobAction)
	mux.HandleFunc("/api/jobs/", server.jobAction)

	addr := ":8080"

	log.Printf(
		"jobs-dashboard listening on %s namespace=%q endpoint=%q maxJobs=%d oauth2-proxy=%t",
		addr,
		namespace,
		endpoint,
		maxJobs,
		access.enabled,
	)

	log.Fatal(http.ListenAndServe(addr, access.middleware(mux)))
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplates.ExecuteTemplate(w, "page", map[string]any{
		"Namespace": s.namespace,
	}); err != nil {
		log.Printf("render dashboard page: %v", err)
	}
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rows, err := s.listCronJobs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	view := DashboardView{CronJobs: rows}
	for _, row := range rows {
		view.JobCount += len(row.Jobs)
		if row.Active > 0 {
			view.ActiveCronJobs++
		}
		if row.Suspended {
			view.Suspended++
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplates.ExecuteTemplate(w, "dashboard", view); err != nil {
		log.Printf("render dashboard fragment: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	_ = json.NewEncoder(w).Encode(value)
}

func errJSON(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{
		"error": message,
	})
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"namespace": s.namespace,
		"endpoint":  s.endpoint,
		"maxJobs":   s.maxJobs,
	})
}

func (s *Server) cronjobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	rows, err := s.listCronJobs(r.Context())
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) listCronJobs(ctx context.Context) ([]CronJobRow, error) {
	items, err := s.client.
		BatchV1().
		CronJobs(s.namespace).
		List(ctx, metav1.ListOptions{})

	if err != nil {
		return nil, err
	}

	rows := make([]CronJobRow, 0, len(items.Items))

	for _, cj := range items.Items {
		jobs, err := s.jobsForCronJob(ctx, &cj)
		if err != nil {
			return nil, err
		}

		var lastSchedule *time.Time

		if cj.Status.LastScheduleTime != nil {
			t := cj.Status.LastScheduleTime.Time
			lastSchedule = &t
		}

		rows = append(rows, CronJobRow{
			Name:         cj.Name,
			Namespace:    cj.Namespace,
			Schedule:     cj.Spec.Schedule,
			Suspended:    cj.Spec.Suspend != nil && *cj.Spec.Suspend,
			Active:       len(cj.Status.Active),
			LastSchedule: lastSchedule,
			Jobs:         jobs,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		left := rows[i].Namespace + "/" + rows[i].Name
		right := rows[j].Namespace + "/" + rows[j].Name

		return left < right
	})

	return rows, nil
}

func (s *Server) jobsForCronJob(
	ctx context.Context,
	cj *batchv1.CronJob,
) ([]JobRow, error) {
	list, err := s.client.
		BatchV1().
		Jobs(cj.Namespace).
		List(ctx, metav1.ListOptions{})

	if err != nil {
		return nil, err
	}

	out := make([]JobRow, 0)

	for _, job := range list.Items {
		owned := false

		for _, owner := range job.OwnerReferences {
			if owner.Kind == "CronJob" && owner.Name == cj.Name {
				owned = true
				break
			}
		}

		if !owned {
			continue
		}

		status := "Pending"

		if job.Status.Failed > 0 {
			status = "Failed"
		} else if job.Status.CompletionTime != nil {
			status = "Completed"
		} else if job.Status.Active > 0 {
			status = "Running"
		}

		var start *time.Time
		var completion *time.Time

		var durationSeconds int64

		if job.Status.StartTime != nil {
			t := job.Status.StartTime.Time
			start = &t
		}

		if job.Status.CompletionTime != nil {
			t := job.Status.CompletionTime.Time
			completion = &t

			if start != nil {
				durationSeconds = int64(
					t.Sub(*start).Seconds(),
				)
			}
		}

		out = append(out, JobRow{
			Name:            job.Name,
			Namespace:       job.Namespace,
			Status:          status,
			Start:           start,
			Completion:      completion,
			DurationSeconds: durationSeconds,
			Succeeded:       job.Status.Succeeded,
			Failed:          job.Status.Failed,
			Active:          job.Status.Active,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name > out[j].Name
	})

	if len(out) > s.maxJobs {
		out = out[:s.maxJobs]
	}

	return out, nil
}

func (s *Server) cronjobAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(
		strings.Trim(r.URL.Path, "/"),
		"/",
	)

	if len(parts) < 3 || len(parts) > 4 || parts[0] != "api" || parts[1] != "cronjobs" {
		errJSON(w, http.StatusNotFound, "not found")
		return
	}

	name := parts[2]
	action := ""
	if len(parts) > 3 {
		action = parts[3]
	}

	ctx := r.Context()
	if action == "" && r.Method == http.MethodPatch {
		var request struct {
			Suspend  *bool   `json:"suspend"`
			Schedule *string `json:"schedule"`
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				errJSON(w, http.StatusBadRequest, "request body must contain a boolean suspend field")
				return
			}
		} else {
			if err := r.ParseForm(); err != nil {
				errJSON(w, http.StatusBadRequest, "request body must contain suspend and/or schedule")
				return
			}
			values := r.Form["suspend"]
			if len(values) == 1 {
				value, err := strconv.ParseBool(values[0])
				if err == nil {
					request.Suspend = &value
				}
			}
			if values, ok := r.Form["schedule"]; ok && len(values) == 1 {
				schedule := values[0]
				request.Schedule = &schedule
			}
		}
		if request.Suspend == nil && request.Schedule == nil {
			errJSON(w, http.StatusBadRequest, "request body must contain suspend and/or schedule")
			return
		}
		if request.Schedule != nil && strings.TrimSpace(*request.Schedule) == "" {
			errJSON(w, http.StatusBadRequest, "schedule must not be empty")
			return
		}

		specPatch := make(map[string]any)
		if request.Suspend != nil {
			specPatch["suspend"] = *request.Suspend
		}
		if request.Schedule != nil {
			specPatch["schedule"] = *request.Schedule
		}
		patch, err := json.Marshal(map[string]any{"spec": specPatch})
		if err != nil {
			errJSON(w, http.StatusInternalServerError, "could not encode CronJob update")
			return
		}

		updated, err := s.client.
			BatchV1().
			CronJobs(s.namespace).
			Patch(
				ctx,
				name,
				types.MergePatchType,
				patch,
				metav1.PatchOptions{},
			)
		if err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.Header().Set("HX-Trigger", "dashboardRefresh")
		writeJSON(w, http.StatusOK, map[string]any{
			"name":     updated.Name,
			"suspend":  updated.Spec.Suspend != nil && *updated.Spec.Suspend,
			"schedule": updated.Spec.Schedule,
		})
		return
	}

	cj, err := s.client.
		BatchV1().
		CronJobs(s.namespace).
		Get(ctx, name, metav1.GetOptions{})

	if err != nil {
		errJSON(w, http.StatusNotFound, err.Error())
		return
	}

	if action == "" && r.Method == http.MethodGet {
		jobs, err := s.jobsForCronJob(ctx, cj)
		if err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		var lastSchedule *time.Time
		if cj.Status.LastScheduleTime != nil {
			value := cj.Status.LastScheduleTime.Time
			lastSchedule = &value
		}
		writeJSON(w, http.StatusOK, CronJobRow{
			Name:         cj.Name,
			Namespace:    cj.Namespace,
			Schedule:     cj.Spec.Schedule,
			Suspended:    cj.Spec.Suspend != nil && *cj.Spec.Suspend,
			Active:       len(cj.Status.Active),
			LastSchedule: lastSchedule,
			Jobs:         jobs,
		})
		return
	}

	switch action {

	case "run":
		if r.Method != http.MethodPost {
			errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: cj.Name + "-manual-",
				Namespace:    cj.Namespace,

				Labels: map[string]string{
					"dashboard-test": "true",
					"manual-run":     "true",
					"cronjob":        cj.Name,
				},

				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "batch/v1",
						Kind:       "CronJob",
						Name:       cj.Name,
						UID:        cj.UID,
					},
				},
			},

			Spec: cj.Spec.JobTemplate.Spec,
		}

		created, err := s.client.
			BatchV1().
			Jobs(cj.Namespace).
			Create(
				ctx,
				job,
				metav1.CreateOptions{},
			)

		if err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.Header().Set("HX-Trigger", "dashboardRefresh")
		writeJSON(w, http.StatusCreated, map[string]string{
			"name":      created.Name,
			"namespace": created.Namespace,
		})

	case "suspend", "resume":

		if r.Method != http.MethodPost {
			errJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		suspend := action == "suspend"

		cj.Spec.Suspend = &suspend

		updated, err := s.client.
			BatchV1().
			CronJobs(s.namespace).
			Update(
				ctx,
				cj,
				metav1.UpdateOptions{},
			)

		if err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.Header().Set("HX-Trigger", "dashboardRefresh")
		writeJSON(w, http.StatusOK, map[string]any{
			"name":    updated.Name,
			"suspend": suspend,
		})

	default:
		errJSON(w, http.StatusNotFound, "unknown action")
	}
}

func (s *Server) jobAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(
		strings.Trim(r.URL.Path, "/"),
		"/",
	)

	if len(parts) != 3 && len(parts) != 4 {
		errJSON(w, http.StatusNotFound, "not found")
		return
	}

	name := parts[2]

	ctx := r.Context()

	if len(parts) == 4 && parts[3] == "logs" && r.Method == http.MethodGet {
		s.jobLogs(w, r, name)
		return
	}
	if len(parts) == 4 && parts[3] == "manifest" && r.Method == http.MethodGet {
		s.jobManifest(w, r, name)
		return
	}

	if len(parts) == 3 && r.Method == http.MethodDelete {
		err := s.client.
			BatchV1().
			Jobs(s.namespace).
			Delete(
				ctx,
				name,
				metav1.DeleteOptions{},
			)

		if err != nil {
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.Header().Set("HX-Trigger", "dashboardRefresh")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	errJSON(
		w,
		http.StatusMethodNotAllowed,
		"method not allowed",
	)
}

func (s *Server) jobLogs(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	job, err := s.client.BatchV1().Jobs(s.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		errJSON(w, http.StatusInternalServerError, fmt.Sprintf("get Job %q: %v", name, err))
		return
	}
	if job.Spec.Selector == nil {
		errJSON(w, http.StatusInternalServerError, "Job does not have a pod selector")
		return
	}

	selector, err := metav1.LabelSelectorAsSelector(job.Spec.Selector)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, fmt.Sprintf("invalid Job pod selector: %v", err))
		return
	}
	pods, err := s.client.CoreV1().Pods(s.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		errJSON(w, http.StatusInternalServerError, fmt.Sprintf("list Job Pods: %v", err))
		return
	}

	var latestPod *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		ownedByJob := false
		for _, owner := range pod.OwnerReferences {
			if owner.Kind == "Job" && owner.UID == job.UID {
				ownedByJob = true
				break
			}
		}
		if !ownedByJob {
			continue
		}
		if latestPod == nil || pod.CreationTimestamp.After(latestPod.CreationTimestamp.Time) {
			latestPod = pod
		}
	}

	if latestPod == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<pre class="job-log-output">No Pods found for this Job yet.</pre>`)
		return
	}

	const (
		maxLogBytes    int64 = 128 * 1024
		logHeaderBytes       = 256
	)
	tailLines := int64(200)
	remainingBytes := maxLogBytes
	var output strings.Builder
	containers := append([]corev1.Container(nil), latestPod.Spec.InitContainers...)
	containers = append(containers, latestPod.Spec.Containers...)
	for _, container := range containers {
		if remainingBytes <= logHeaderBytes {
			break
		}
		header := fmt.Sprintf("=== %s/%s ===\n", latestPod.Name, container.Name)
		output.WriteString(header)
		remainingBytes -= int64(len(header))

		limitBytes := remainingBytes
		rawLogs, err := s.client.CoreV1().Pods(s.namespace).GetLogs(latestPod.Name, &corev1.PodLogOptions{
			Container:  container.Name,
			TailLines:  &tailLines,
			LimitBytes: &limitBytes,
			Timestamps: true,
		}).DoRaw(ctx)
		if err != nil {
			errJSON(w, http.StatusInternalServerError, fmt.Sprintf("get logs for %s/%s: %v", latestPod.Name, container.Name, err))
			return
		}
		output.Write(rawLogs)
		remainingBytes -= int64(len(rawLogs))
		if int64(len(rawLogs)) >= limitBytes {
			output.WriteString("\n[log output truncated]")
			break
		}
		if len(rawLogs) > 0 && rawLogs[len(rawLogs)-1] != '\n' {
			output.WriteByte('\n')
		}
	}

	if len(containers) == 0 {
		output.WriteString("No containers found in the latest Pod.")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<pre class="job-log-output">%s</pre>`, html.EscapeString(output.String()))
}

func (s *Server) jobManifest(w http.ResponseWriter, r *http.Request, name string) {
	job, err := s.client.BatchV1().Jobs(s.namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		errJSON(w, http.StatusInternalServerError, fmt.Sprintf("get Job %q: %v", name, err))
		return
	}

	metadata := *job.ObjectMeta.DeepCopy()
	metadata.ResourceVersion = ""
	metadata.UID = ""
	metadata.CreationTimestamp = metav1.Time{}
	metadata.DeletionTimestamp = nil
	metadata.DeletionGracePeriodSeconds = nil
	metadata.Generation = 0
	metadata.ManagedFields = nil
	metadata.SelfLink = ""
	manifest := struct {
		APIVersion string            `json:"apiVersion"`
		Kind       string            `json:"kind"`
		Metadata   metav1.ObjectMeta `json:"metadata"`
		Spec       batchv1.JobSpec   `json:"spec"`
	}{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Metadata:   metadata,
		Spec:       job.Spec,
	}

	encoded, err := yaml.Marshal(manifest)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, fmt.Sprintf("encode Job manifest: %v", err))
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<pre class="job-log-output">%s</pre>`, html.EscapeString(string(encoded)))
}
