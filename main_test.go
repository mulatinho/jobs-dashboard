package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func testServer(cronjob *batchv1.CronJob) *Server {
	return &Server{
		client:    fake.NewSimpleClientset(cronjob),
		namespace: "default",
		maxJobs:   10,
	}
}

func TestHomeRendersDashboard(t *testing.T) {
	server := &Server{namespace: "default"}
	response := httptest.NewRecorder()
	server.home(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET / returned status %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "Jobs &amp; schedules") {
		t.Fatal("GET / did not render the dashboard")
	}
}

func TestCronJobGetAndPatchActions(t *testing.T) {
	cronjob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "daily-report", Namespace: "default"},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 9 * * *",
		},
	}
	server := testServer(cronjob)

	getResponse := httptest.NewRecorder()
	server.cronjobAction(
		getResponse,
		httptest.NewRequest(http.MethodGet, "/api/cronjobs/daily-report", nil),
	)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("GET CronJob returned status %d: %s", getResponse.Code, getResponse.Body.String())
	}

	patchResponse := httptest.NewRecorder()
	patchRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/cronjobs/daily-report",
		strings.NewReader(`{"suspend":true,"schedule":"0 9 * * *"}`),
	)
	patchRequest.Header.Set("Content-Type", "application/json")
	server.cronjobAction(patchResponse, patchRequest)
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("PATCH CronJob returned status %d: %s", patchResponse.Code, patchResponse.Body.String())
	}

	updated, err := server.client.BatchV1().CronJobs("default").Get(
		patchRequest.Context(),
		"daily-report",
		metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("read patched CronJob: %v", err)
	}
	if updated.Spec.Suspend == nil || !*updated.Spec.Suspend {
		t.Fatal("PATCH did not suspend the CronJob")
	}
	if updated.Spec.Schedule != "0 9 * * *" {
		t.Fatalf("JSON PATCH changed raw schedule to %q", updated.Spec.Schedule)
	}

	formResponse := httptest.NewRecorder()
	formRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/cronjobs/daily-report",
		strings.NewReader("suspend=false&schedule=%2A%2F5+*+*+*+*"),
	)
	formRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.cronjobAction(formResponse, formRequest)
	if formResponse.Code != http.StatusOK {
		t.Fatalf("HTMX PATCH returned status %d: %s", formResponse.Code, formResponse.Body.String())
	}

	updated, err = server.client.BatchV1().CronJobs("default").Get(
		formRequest.Context(),
		"daily-report",
		metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("read updated CronJob: %v", err)
	}
	if updated.Spec.Schedule != "*/5 * * * *" {
		t.Fatalf("PATCH changed raw schedule to %q", updated.Spec.Schedule)
	}
	if updated.Spec.Suspend == nil || *updated.Spec.Suspend {
		t.Fatal("PATCH did not resume the CronJob")
	}
}

func TestDashboardDisplaysRawCronExpression(t *testing.T) {
	server := testServer(&batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "daily-report", Namespace: "default"},
		Spec:       batchv1.CronJobSpec{Schedule: "*/5 * * * *"},
	})
	response := httptest.NewRecorder()
	server.dashboard(response, httptest.NewRequest(http.MethodGet, "/partials/dashboard", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("GET dashboard fragment returned status %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `class="schedule-value">*/5 * * * *</code>`) {
		t.Fatal("dashboard did not display the original cron expression")
	}
	if !strings.Contains(response.Body.String(), `value="*/5 * * * *"`) {
		t.Fatal("schedule editor was not populated with the original cron expression")
	}
}

func TestJobLogsDropdownLoadsNewestOwnedPodLogs(t *testing.T) {
	cronjob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "default"},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nightly-123",
			Namespace: "default",
			UID:       types.UID("job-uid"),
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "batch/v1", Kind: "CronJob", Name: "nightly", UID: types.UID("cronjob-uid")},
			},
		},
		Spec: batchv1.JobSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"batch.kubernetes.io/controller-uid": "job-uid"},
			},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers:    []corev1.Container{{Name: "worker", Image: "example/worker"}},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nightly-123-pod",
			Namespace:         "default",
			CreationTimestamp: metav1.NewTime(time.Unix(1, 0)),
			Labels:            map[string]string{"batch.kubernetes.io/controller-uid": "job-uid"},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Job", Name: job.Name, UID: job.UID},
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "worker", Image: "example/worker"}},
		},
	}
	otherJobPod := pod.DeepCopy()
	otherJobPod.Name = "newer-pod-from-another-job"
	otherJobPod.CreationTimestamp = metav1.NewTime(time.Unix(2, 0))
	otherJobPod.OwnerReferences[0].UID = types.UID("another-job-uid")
	otherJobPod.Spec.Containers[0].Name = "other-worker"
	newestJobPod := pod.DeepCopy()
	newestJobPod.Name = "newest-job-pod"
	newestJobPod.CreationTimestamp = metav1.NewTime(time.Unix(3, 0))
	newestJobPod.Spec.Containers[0].Name = "newest-worker"
	client := fake.NewSimpleClientset(cronjob, job, pod, otherJobPod, newestJobPod)
	var requestedLogs *corev1.PodLogOptions
	client.PrependReactor("get", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "log" {
			requestedLogs = action.(clienttesting.GenericAction).GetValue().(*corev1.PodLogOptions)
		}
		return false, nil, nil
	})
	server := &Server{client: client, namespace: "default", maxJobs: 10}

	dashboardResponse := httptest.NewRecorder()
	server.dashboard(dashboardResponse, httptest.NewRequest(http.MethodGet, "/partials/dashboard", nil))
	if dashboardResponse.Code != http.StatusOK {
		t.Fatalf("GET dashboard returned status %d: %s", dashboardResponse.Code, dashboardResponse.Body.String())
	}
	if !strings.Contains(dashboardResponse.Body.String(), `hx-get="/api/jobs/nightly-123/logs"`) ||
		!strings.Contains(dashboardResponse.Body.String(), `hx-get="/api/jobs/nightly-123/manifest"`) ||
		!strings.Contains(dashboardResponse.Body.String(), "Latest logs") ||
		!strings.Contains(dashboardResponse.Body.String(), ">Manifest</summary>") {
		t.Fatal("dashboard did not render the lazy-loaded Job logs and manifest dropdowns")
	}

	logsResponse := httptest.NewRecorder()
	server.jobAction(logsResponse, httptest.NewRequest(http.MethodGet, "/api/jobs/nightly-123/logs", nil))
	if logsResponse.Code != http.StatusOK {
		t.Fatalf("GET Job logs returned status %d: %s", logsResponse.Code, logsResponse.Body.String())
	}
	if !strings.Contains(logsResponse.Body.String(), "fake logs") {
		t.Fatal("Job logs response did not contain the pod logs")
	}
	if requestedLogs == nil || requestedLogs.Container != "newest-worker" ||
		requestedLogs.TailLines == nil || *requestedLogs.TailLines != 200 {
		t.Fatalf("unexpected Kubernetes log request options: %#v", requestedLogs)
	}

	manifestResponse := httptest.NewRecorder()
	server.jobAction(manifestResponse, httptest.NewRequest(http.MethodGet, "/api/jobs/nightly-123/manifest", nil))
	if manifestResponse.Code != http.StatusOK {
		t.Fatalf("GET Job manifest returned status %d: %s", manifestResponse.Code, manifestResponse.Body.String())
	}
	manifest := manifestResponse.Body.String()
	for _, expected := range []string{
		"apiVersion: batch/v1",
		"kind: Job",
		"name: nightly-123",
		"image: example/worker",
	} {
		if !strings.Contains(manifest, expected) {
			t.Errorf("Job manifest did not contain %q: %s", expected, manifest)
		}
	}
	if strings.Contains(manifest, "status:") || strings.Contains(manifest, "resourceVersion:") {
		t.Fatalf("Job manifest contains runtime or server-managed fields: %s", manifest)
	}
}
