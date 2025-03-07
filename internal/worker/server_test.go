package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/osbuild/osbuild-composer/internal/distro"
	"github.com/osbuild/osbuild-composer/internal/distro/test_distro"
	"github.com/osbuild/osbuild-composer/internal/jobqueue"
	"github.com/osbuild/osbuild-composer/internal/jobqueue/fsjobqueue"
	"github.com/osbuild/osbuild-composer/internal/osbuild2"
	"github.com/osbuild/osbuild-composer/internal/test"
	"github.com/osbuild/osbuild-composer/internal/worker"
	"github.com/osbuild/osbuild-composer/internal/worker/clienterrors"
)

func newTestServer(t *testing.T, tempdir string, jobRequestTimeout time.Duration, basePath string) *worker.Server {
	q, err := fsjobqueue.New(tempdir)
	if err != nil {
		t.Fatalf("error creating fsjobqueue: %v", err)
	}
	return worker.NewServer(nil, q, "", jobRequestTimeout, basePath)
}

// Ensure that the status request returns OK.
func TestStatus(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	server := newTestServer(t, tempdir, time.Duration(0), "/api/worker/v1")
	handler := server.Handler()
	test.TestRoute(t, handler, false, "GET", "/api/worker/v1/status", ``, http.StatusOK, `{"status":"OK", "href": "/api/worker/v1/status", "kind":"Status"}`, "message", "id")
}

func TestErrors(t *testing.T) {
	var cases = []struct {
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
	}{
		// Bogus path
		{"GET", "/api/worker/v1/foo", ``, http.StatusNotFound},
		// Create job with invalid body
		{"POST", "/api/worker/v1/jobs", ``, http.StatusBadRequest},
		// Wrong method
		{"GET", "/api/worker/v1/jobs", ``, http.StatusMethodNotAllowed},
		// Update job with invalid ID
		{"PATCH", "/api/worker/v1/jobs/foo", `{"status":"FINISHED"}`, http.StatusBadRequest},
		// Update job that does not exist, with invalid body
		{"PATCH", "/api/worker/v1/jobs/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", ``, http.StatusBadRequest},
		// Update job that does not exist
		{"PATCH", "/api/worker/v1/jobs/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", `{"status":"FINISHED"}`, http.StatusNotFound},
	}

	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	for _, c := range cases {
		server := newTestServer(t, tempdir, time.Duration(0), "/api/worker/v1")
		handler := server.Handler()
		test.TestRoute(t, handler, false, c.Method, c.Path, c.Body, c.ExpectedStatus, `{"kind":"Error"}`, "message", "href", "operation_id", "reason", "id", "code")
	}
}

func TestErrorsAlteredBasePath(t *testing.T) {
	var cases = []struct {
		Method         string
		Path           string
		Body           string
		ExpectedStatus int
	}{
		// Bogus path
		{"GET", "/api/image-builder-worker/v1/foo", ``, http.StatusNotFound},
		// Create job with invalid body
		{"POST", "/api/image-builder-worker/v1/jobs", ``, http.StatusBadRequest},
		// Wrong method
		{"GET", "/api/image-builder-worker/v1/jobs", ``, http.StatusMethodNotAllowed},
		// Update job with invalid ID
		{"PATCH", "/api/image-builder-worker/v1/jobs/foo", `{"status":"FINISHED"}`, http.StatusBadRequest},
		// Update job that does not exist, with invalid body
		{"PATCH", "/api/image-builder-worker/v1/jobs/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", ``, http.StatusBadRequest},
		// Update job that does not exist
		{"PATCH", "/api/image-builder-worker/v1/jobs/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", `{"status":"FINISHED"}`, http.StatusNotFound},
	}

	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	for _, c := range cases {
		server := newTestServer(t, tempdir, time.Duration(0), "/api/image-builder-worker/v1")
		handler := server.Handler()
		test.TestRoute(t, handler, false, c.Method, c.Path, c.Body, c.ExpectedStatus, `{"kind":"Error"}`, "message", "href", "operation_id", "reason", "id", "code")
	}
}

func TestCreate(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	distroStruct := test_distro.New()
	arch, err := distroStruct.GetArch(test_distro.TestArchName)
	if err != nil {
		t.Fatalf("error getting arch from distro: %v", err)
	}
	imageType, err := arch.GetImageType(test_distro.TestImageTypeName)
	if err != nil {
		t.Fatalf("error getting image type from arch: %v", err)
	}
	manifest, err := imageType.Manifest(nil, distro.ImageOptions{Size: imageType.Size(0)}, nil, nil, 0)
	if err != nil {
		t.Fatalf("error creating osbuild manifest: %v", err)
	}
	server := newTestServer(t, tempdir, time.Duration(0), "/api/worker/v1")
	handler := server.Handler()

	_, err = server.EnqueueOSBuild(arch.Name(), &worker.OSBuildJob{Manifest: manifest})
	require.NoError(t, err)

	test.TestRoute(t, handler, false, "POST", "/api/worker/v1/jobs",
		fmt.Sprintf(`{"types":["osbuild"],"arch":"%s"}`, test_distro.TestArchName), http.StatusCreated,
		`{"kind":"RequestJob","href":"/api/worker/v1/jobs","type":"osbuild","args":{"manifest":{"pipeline":{},"sources":{}}}}`, "id", "location", "artifact_location")
}

func TestCancel(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	distroStruct := test_distro.New()
	arch, err := distroStruct.GetArch(test_distro.TestArchName)
	if err != nil {
		t.Fatalf("error getting arch from distro: %v", err)
	}
	imageType, err := arch.GetImageType(test_distro.TestImageTypeName)
	if err != nil {
		t.Fatalf("error getting image type from arch: %v", err)
	}
	manifest, err := imageType.Manifest(nil, distro.ImageOptions{Size: imageType.Size(0)}, nil, nil, 0)
	if err != nil {
		t.Fatalf("error creating osbuild manifest: %v", err)
	}
	server := newTestServer(t, tempdir, time.Duration(0), "/api/worker/v1")
	handler := server.Handler()

	jobId, err := server.EnqueueOSBuild(arch.Name(), &worker.OSBuildJob{Manifest: manifest})
	require.NoError(t, err)

	j, token, typ, args, dynamicArgs, err := server.RequestJob(context.Background(), arch.Name(), []string{"osbuild"})
	require.NoError(t, err)
	require.Equal(t, jobId, j)
	require.Equal(t, "osbuild", typ)
	require.NotNil(t, args)
	require.Nil(t, dynamicArgs)

	test.TestRoute(t, handler, false, "GET", fmt.Sprintf("/api/worker/v1/jobs/%s", token), `{}`, http.StatusOK,
		fmt.Sprintf(`{"canceled":false,"href":"/api/worker/v1/jobs/%s","id":"%s","kind":"JobStatus"}`, token, token))

	err = server.Cancel(jobId)
	require.NoError(t, err)

	test.TestRoute(t, handler, false, "GET", fmt.Sprintf("/api/worker/v1/jobs/%s", token), `{}`, http.StatusOK,
		fmt.Sprintf(`{"canceled":true,"href":"/api/worker/v1/jobs/%s","id":"%s","kind":"JobStatus"}`, token, token))
}

func TestUpdate(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	distroStruct := test_distro.New()
	arch, err := distroStruct.GetArch(test_distro.TestArchName)
	if err != nil {
		t.Fatalf("error getting arch from distro: %v", err)
	}
	imageType, err := arch.GetImageType(test_distro.TestImageTypeName)
	if err != nil {
		t.Fatalf("error getting image type from arch: %v", err)
	}
	manifest, err := imageType.Manifest(nil, distro.ImageOptions{Size: imageType.Size(0)}, nil, nil, 0)
	if err != nil {
		t.Fatalf("error creating osbuild manifest: %v", err)
	}
	server := newTestServer(t, tempdir, time.Duration(0), "/api/worker/v1")
	handler := server.Handler()

	jobId, err := server.EnqueueOSBuild(arch.Name(), &worker.OSBuildJob{Manifest: manifest})
	require.NoError(t, err)

	j, token, typ, args, dynamicArgs, err := server.RequestJob(context.Background(), arch.Name(), []string{"osbuild"})
	require.NoError(t, err)
	require.Equal(t, jobId, j)
	require.Equal(t, "osbuild", typ)
	require.NotNil(t, args)
	require.Nil(t, dynamicArgs)

	test.TestRoute(t, handler, false, "PATCH", fmt.Sprintf("/api/worker/v1/jobs/%s", token), `{}`, http.StatusOK,
		fmt.Sprintf(`{"href":"/api/worker/v1/jobs/%s","id":"%s","kind":"UpdateJobResponse"}`, token, token))
	test.TestRoute(t, handler, false, "PATCH", fmt.Sprintf("/api/worker/v1/jobs/%s", token), `{}`, http.StatusNotFound,
		`{"href":"/api/worker/v1/errors/5","code":"IMAGE-BUILDER-WORKER-5","id":"5","kind":"Error","message":"Token not found","reason":"Token not found"}`,
		"operation_id")
}

func TestArgs(t *testing.T) {
	distroStruct := test_distro.New()
	arch, err := distroStruct.GetArch(test_distro.TestArchName)
	require.NoError(t, err)
	imageType, err := arch.GetImageType(test_distro.TestImageTypeName)
	require.NoError(t, err)
	manifest, err := imageType.Manifest(nil, distro.ImageOptions{Size: imageType.Size(0)}, nil, nil, 0)
	require.NoError(t, err)

	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)
	server := newTestServer(t, tempdir, time.Duration(0), "/api/worker/v1")

	job := worker.OSBuildJob{
		Manifest:  manifest,
		ImageName: "test-image",
		PipelineNames: &worker.PipelineNames{
			Build:   []string{"b"},
			Payload: []string{"x", "y", "z"},
		},
	}
	jobId, err := server.EnqueueOSBuild(arch.Name(), &job)
	require.NoError(t, err)

	_, _, _, args, _, err := server.RequestJob(context.Background(), arch.Name(), []string{"osbuild"})
	require.NoError(t, err)
	require.NotNil(t, args)

	var jobArgs worker.OSBuildJob
	jobType, rawArgs, deps, err := server.Job(jobId, &jobArgs)
	require.NoError(t, err)
	require.Equal(t, args, rawArgs)
	require.Equal(t, job, jobArgs)
	require.Equal(t, jobType, "osbuild:"+arch.Name())
	require.Equal(t, []uuid.UUID(nil), deps)
}

func TestUpload(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	distroStruct := test_distro.New()
	arch, err := distroStruct.GetArch(test_distro.TestArchName)
	if err != nil {
		t.Fatalf("error getting arch from distro: %v", err)
	}
	imageType, err := arch.GetImageType(test_distro.TestImageTypeName)
	if err != nil {
		t.Fatalf("error getting image type from arch: %v", err)
	}
	manifest, err := imageType.Manifest(nil, distro.ImageOptions{Size: imageType.Size(0)}, nil, nil, 0)
	if err != nil {
		t.Fatalf("error creating osbuild manifest: %v", err)
	}
	server := newTestServer(t, tempdir, time.Duration(0), "/api/worker/v1")
	handler := server.Handler()

	jobID, err := server.EnqueueOSBuild(arch.Name(), &worker.OSBuildJob{Manifest: manifest})
	require.NoError(t, err)

	j, token, typ, args, dynamicArgs, err := server.RequestJob(context.Background(), arch.Name(), []string{"osbuild"})
	require.NoError(t, err)
	require.Equal(t, jobID, j)
	require.Equal(t, "osbuild", typ)
	require.NotNil(t, args)
	require.Nil(t, dynamicArgs)

	test.TestRoute(t, handler, false, "PUT", fmt.Sprintf("/api/worker/v1/jobs/%s/artifacts/foobar", token), `this is my artifact`, http.StatusOK, `?`)
}

func TestUploadAlteredBasePath(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	distroStruct := test_distro.New()
	arch, err := distroStruct.GetArch(test_distro.TestArchName)
	if err != nil {
		t.Fatalf("error getting arch from distro: %v", err)
	}
	imageType, err := arch.GetImageType(test_distro.TestImageTypeName)
	if err != nil {
		t.Fatalf("error getting image type from arch: %v", err)
	}
	manifest, err := imageType.Manifest(nil, distro.ImageOptions{Size: imageType.Size(0)}, nil, nil, 0)
	if err != nil {
		t.Fatalf("error creating osbuild manifest: %v", err)
	}
	server := newTestServer(t, tempdir, time.Duration(0), "/api/image-builder-worker/v1")
	handler := server.Handler()

	jobID, err := server.EnqueueOSBuild(arch.Name(), &worker.OSBuildJob{Manifest: manifest})
	require.NoError(t, err)

	j, token, typ, args, dynamicArgs, err := server.RequestJob(context.Background(), arch.Name(), []string{"osbuild"})
	require.NoError(t, err)
	require.Equal(t, jobID, j)
	require.Equal(t, "osbuild", typ)
	require.NotNil(t, args)
	require.Nil(t, dynamicArgs)

	test.TestRoute(t, handler, false, "PUT", fmt.Sprintf("/api/image-builder-worker/v1/jobs/%s/artifacts/foobar", token), `this is my artifact`, http.StatusOK, `?`)
}

func TestOAuth(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	q, err := fsjobqueue.New(tempdir)
	require.NoError(t, err)
	workerServer := worker.NewServer(nil, q, tempdir, time.Duration(0), "/api/image-builder-worker/v1")
	handler := workerServer.Handler()

	workSrv := httptest.NewServer(handler)
	defer workSrv.Close()

	/* Check that the worker supplies the access token  */
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer accessToken!", r.Header.Get("Authorization"))
		handler.ServeHTTP(w, r)
	}))
	defer proxySrv.Close()

	offlineToken := "someOfflineToken"
	/* Start oauth srv supplying the bearer token */
	oauthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		err = r.ParseForm()
		require.NoError(t, err)

		require.Equal(t, "refresh_token", r.FormValue("grant_type"))
		require.Equal(t, "rhsm-api", r.FormValue("client_id"))
		require.Equal(t, offlineToken, r.FormValue("refresh_token"))

		bt := struct {
			AccessToken     string `json:"access_token"`
			ValidForSeconds int    `json:"expires_in"`
		}{
			"accessToken!",
			900,
		}

		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(bt)
		require.NoError(t, err)
	}))
	defer oauthSrv.Close()

	distroStruct := test_distro.New()
	arch, err := distroStruct.GetArch(test_distro.TestArchName)
	if err != nil {
		t.Fatalf("error getting arch from distro: %v", err)
	}
	imageType, err := arch.GetImageType(test_distro.TestImageTypeName)
	if err != nil {
		t.Fatalf("error getting image type from arch: %v", err)
	}
	manifest, err := imageType.Manifest(nil, distro.ImageOptions{Size: imageType.Size(0)}, nil, nil, 0)
	if err != nil {
		t.Fatalf("error creating osbuild manifest: %v", err)
	}

	_, err = workerServer.EnqueueOSBuild(arch.Name(), &worker.OSBuildJob{Manifest: manifest})
	require.NoError(t, err)

	client, err := worker.NewClient(proxySrv.URL, nil, &offlineToken, &oauthSrv.URL, "/api/image-builder-worker/v1")
	require.NoError(t, err)
	job, err := client.RequestJob([]string{"osbuild"}, arch.Name())
	require.NoError(t, err)
	r := strings.NewReader("artifact contents")
	require.NoError(t, job.UploadArtifact("some-artifact", r))
	c, err := job.Canceled()
	require.False(t, c)
}

func TestTimeout(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	distroStruct := test_distro.New()
	arch, err := distroStruct.GetArch(test_distro.TestArchName)
	if err != nil {
		t.Fatalf("error getting arch from distro: %v", err)
	}
	server := newTestServer(t, tempdir, time.Millisecond*10, "/api/image-builder-worker/v1")

	_, _, _, _, _, err = server.RequestJob(context.Background(), arch.Name(), []string{"osbuild"})
	require.Equal(t, jobqueue.ErrDequeueTimeout, err)

	test.TestRoute(t, server.Handler(), false, "POST", "/api/image-builder-worker/v1/jobs", `{"arch":"arch","types":["types"]}`, http.StatusNoContent,
		`{"href":"/api/image-builder-worker/v1/jobs","id":"00000000-0000-0000-0000-000000000000","kind":"RequestJob"}`)
}

func TestRequestJobById(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	distroStruct := test_distro.New()
	arch, err := distroStruct.GetArch(test_distro.TestArchName)
	if err != nil {
		t.Fatalf("error getting arch from distro: %v", err)
	}
	server := newTestServer(t, tempdir, time.Duration(0), "/api/worker/v1")
	handler := server.Handler()

	depsolveJobId, err := server.EnqueueDepsolve(&worker.DepsolveJob{})
	require.NoError(t, err)

	jobId, err := server.EnqueueManifestJobByID(&worker.ManifestJobByID{}, depsolveJobId)
	require.NoError(t, err)

	test.TestRoute(t, server.Handler(), false, "POST", "/api/worker/v1/jobs", `{"arch":"arch","types":["manifest-id-only"]}`, http.StatusBadRequest,
		`{"href":"/api/worker/v1/errors/15","kind":"Error","id": "15","code":"IMAGE-BUILDER-WORKER-15"}`, "operation_id", "reason", "message")

	_, _, _, _, _, err = server.RequestJobById(context.Background(), arch.Name(), jobId)
	require.Error(t, jobqueue.ErrNotPending, err)

	_, token, _, _, _, err := server.RequestJob(context.Background(), arch.Name(), []string{"depsolve"})
	require.NoError(t, err)

	depsolveJR, err := json.Marshal(worker.DepsolveJobResult{})
	require.NoError(t, err)
	err = server.FinishJob(token, depsolveJR)
	require.NoError(t, err)

	j, token, typ, args, dynamicArgs, err := server.RequestJobById(context.Background(), arch.Name(), jobId)
	require.NoError(t, err)
	require.Equal(t, jobId, j)
	require.Equal(t, "manifest-id-only", typ)
	require.NotNil(t, args)
	require.NotNil(t, dynamicArgs)

	test.TestRoute(t, handler, false, "GET", fmt.Sprintf("/api/worker/v1/jobs/%s", token), `{}`, http.StatusOK,
		fmt.Sprintf(`{"canceled":false,"href":"/api/worker/v1/jobs/%s","id":"%s","kind":"JobStatus"}`, token, token))
}

// Enqueue OSBuild jobs with and without additional data and read them off the queue to
// check if the fallbacks are added for the old job and the new data are kept
// for the new job.
func TestMixedOSBuildJob(t *testing.T) {
	require := require.New(t)
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(err)
	defer os.RemoveAll(tempdir)

	emptyManifestV2 := distro.Manifest(`{"version":"2","pipelines":{}}`)
	server := newTestServer(t, tempdir, time.Millisecond*10, "/")
	fbPipelines := &worker.PipelineNames{Build: distro.BuildPipelinesFallback(), Payload: distro.PayloadPipelinesFallback()}

	oldJob := worker.OSBuildJob{
		Manifest:  emptyManifestV2,
		ImageName: "no-pipeline-names",
	}
	oldJobID, err := server.EnqueueOSBuild("x", &oldJob)
	require.NoError(err)

	newJob := worker.OSBuildJob{
		Manifest:  emptyManifestV2,
		ImageName: "with-pipeline-names",
		PipelineNames: &worker.PipelineNames{
			Build:   []string{"build"},
			Payload: []string{"other", "pipelines"},
		},
	}
	newJobID, err := server.EnqueueOSBuild("x", &newJob)
	require.NoError(err)

	oldJobRead := new(worker.OSBuildJob)
	_, _, _, err = server.Job(oldJobID, oldJobRead)
	require.NoError(err)
	require.NotNil(oldJobRead.PipelineNames)
	// OldJob gets default pipeline names when read
	require.Equal(fbPipelines, oldJobRead.PipelineNames)
	require.Equal(oldJob.Manifest, oldJobRead.Manifest)
	require.Equal(oldJob.ImageName, oldJobRead.ImageName)
	// Not entirely equal
	require.NotEqual(oldJob, oldJobRead)

	// NewJob the same when read back
	newJobRead := new(worker.OSBuildJob)
	_, _, _, err = server.Job(newJobID, newJobRead)
	require.NoError(err)
	require.NotNil(newJobRead.PipelineNames)
	require.Equal(newJob.PipelineNames, newJobRead.PipelineNames)

	// Dequeue the jobs (via RequestJob) to get their tokens and update them to
	// test the result retrieval

	getJob := func() (uuid.UUID, uuid.UUID) {
		// don't block forever if the jobs weren't added or can't be retrieved
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		id, token, _, _, _, err := server.RequestJob(ctx, "x", []string{"osbuild"})
		require.NoError(err)
		return id, token
	}

	getJobTokens := func(n uint) map[uuid.UUID]uuid.UUID {
		tokens := make(map[uuid.UUID]uuid.UUID, n)
		for idx := uint(0); idx < n; idx++ {
			id, token := getJob()
			tokens[id] = token
		}
		return tokens
	}

	jobTokens := getJobTokens(2)
	// make sure we got them both as expected
	require.Contains(jobTokens, oldJobID)
	require.Contains(jobTokens, newJobID)

	oldJobResult := &worker.OSBuildJobResult{
		Success: true,
		OSBuildOutput: &osbuild2.Result{
			Type:    "result",
			Success: true,
			Log: map[string]osbuild2.PipelineResult{
				"build-old": {
					osbuild2.StageResult{
						ID:      "---",
						Type:    "org.osbuild.test",
						Output:  "<test output>",
						Success: true,
					},
				},
			},
		},
	}
	oldJobResultRaw, err := json.Marshal(oldJobResult)
	require.NoError(err)
	oldJobToken := jobTokens[oldJobID]
	err = server.FinishJob(oldJobToken, oldJobResultRaw)
	require.NoError(err)

	oldJobResultRead := new(worker.OSBuildJobResult)
	_, _, err = server.JobStatus(oldJobID, oldJobResultRead)
	require.NoError(err)

	// oldJobResultRead should have PipelineNames now
	require.NotEqual(oldJobResult, oldJobResultRead)
	require.Equal(fbPipelines, oldJobResultRead.PipelineNames)
	require.NotNil(oldJobResultRead.PipelineNames)
	require.Equal(oldJobResult.OSBuildOutput, oldJobResultRead.OSBuildOutput)
	require.Equal(oldJobResult.Success, oldJobResultRead.Success)

	newJobResult := &worker.OSBuildJobResult{
		Success: true,
		PipelineNames: &worker.PipelineNames{
			Build:   []string{"build-result"},
			Payload: []string{"result-test-payload", "result-test-assembler"},
		},
		OSBuildOutput: &osbuild2.Result{
			Type:    "result",
			Success: true,
			Log: map[string]osbuild2.PipelineResult{
				"build-new": {
					osbuild2.StageResult{
						ID:      "---",
						Type:    "org.osbuild.test",
						Output:  "<test output new>",
						Success: true,
					},
				},
			},
		},
	}
	newJobResultRaw, err := json.Marshal(newJobResult)
	require.NoError(err)
	newJobToken := jobTokens[newJobID]
	err = server.FinishJob(newJobToken, newJobResultRaw)
	require.NoError(err)

	newJobResultRead := new(worker.OSBuildJobResult)
	_, _, err = server.JobStatus(newJobID, newJobResultRead)
	require.NoError(err)
	require.Equal(newJobResult, newJobResultRead)
}

// Enqueue Koji jobs with and without additional data and read them off the queue to
// check if the fallbacks are added for the old job and the new data are kept
// for the new job.
func TestMixedOSBuildKojiJob(t *testing.T) {
	require := require.New(t)
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(err)
	defer os.RemoveAll(tempdir)

	emptyManifestV2 := distro.Manifest(`{"version":"2","pipelines":{}}`)
	server := newTestServer(t, tempdir, time.Duration(0), "/api/worker/v1")
	fbPipelines := &worker.PipelineNames{Build: distro.BuildPipelinesFallback(), Payload: distro.PayloadPipelinesFallback()}

	enqueueKojiJob := func(job *worker.OSBuildKojiJob) uuid.UUID {
		initJob := new(worker.KojiInitJob)
		initJobID, err := server.EnqueueKojiInit(initJob)
		require.NoError(err)
		jobID, err := server.EnqueueOSBuildKoji("k", job, initJobID)
		require.NoError(err)
		return jobID
	}
	oldJob := worker.OSBuildKojiJob{
		Manifest:  emptyManifestV2,
		ImageName: "no-pipeline-names",
	}
	oldJobID := enqueueKojiJob(&oldJob)

	newJob := worker.OSBuildKojiJob{
		Manifest:  emptyManifestV2,
		ImageName: "with-pipeline-names",
		PipelineNames: &worker.PipelineNames{
			Build:   []string{"build"},
			Payload: []string{"other", "pipelines"},
		},
	}
	newJobID := enqueueKojiJob(&newJob)

	oldJobRead := new(worker.OSBuildKojiJob)
	_, _, _, err = server.Job(oldJobID, oldJobRead)
	require.NoError(err)
	require.NotNil(oldJobRead.PipelineNames)
	// OldJob gets default pipeline names when read
	require.Equal(fbPipelines, oldJobRead.PipelineNames)
	require.Equal(oldJob.Manifest, oldJobRead.Manifest)
	require.Equal(oldJob.ImageName, oldJobRead.ImageName)
	// Not entirely equal
	require.NotEqual(oldJob, oldJobRead)

	// NewJob the same when read back
	newJobRead := new(worker.OSBuildKojiJob)
	_, _, _, err = server.Job(newJobID, newJobRead)
	require.NoError(err)
	require.NotNil(newJobRead.PipelineNames)
	require.Equal(newJob.PipelineNames, newJobRead.PipelineNames)

	// Dequeue the jobs (via RequestJob) to get their tokens and update them to
	// test the result retrieval

	// Finish init jobs
	for idx := uint(0); idx < 2; idx++ {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_, token, _, _, _, err := server.RequestJob(ctx, "k", []string{"koji-init"})
		require.NoError(err)
		require.NoError(server.FinishJob(token, nil))
	}

	getJob := func() (uuid.UUID, uuid.UUID) {
		// don't block forever if the jobs weren't added or can't be retrieved
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		id, token, _, _, _, err := server.RequestJob(ctx, "k", []string{"osbuild-koji"})
		require.NoError(err)
		return id, token
	}

	getJobTokens := func(n uint) map[uuid.UUID]uuid.UUID {
		tokens := make(map[uuid.UUID]uuid.UUID, n)
		for idx := uint(0); idx < n; idx++ {
			id, token := getJob()
			tokens[id] = token
		}
		return tokens
	}

	jobTokens := getJobTokens(2)
	// make sure we got them both as expected
	require.Contains(jobTokens, oldJobID)
	require.Contains(jobTokens, newJobID)

	oldJobResult := &worker.OSBuildKojiJobResult{
		HostOS: "rhel-10",
		Arch:   "k",
		OSBuildOutput: &osbuild2.Result{
			Type:    "result",
			Success: true,
			Log: map[string]osbuild2.PipelineResult{
				"build-old": {
					osbuild2.StageResult{
						ID:      "---",
						Type:    "org.osbuild.test",
						Output:  "<test output>",
						Success: true,
					},
				},
			},
		},
	}
	oldJobResultRaw, err := json.Marshal(oldJobResult)
	require.NoError(err)
	oldJobToken := jobTokens[oldJobID]
	err = server.FinishJob(oldJobToken, oldJobResultRaw)
	require.NoError(err)

	oldJobResultRead := new(worker.OSBuildKojiJobResult)
	_, _, err = server.JobStatus(oldJobID, oldJobResultRead)
	require.NoError(err)

	// oldJobResultRead should have PipelineNames now
	require.NotEqual(oldJobResult, oldJobResultRead)
	require.Equal(fbPipelines, oldJobResultRead.PipelineNames)
	require.NotNil(oldJobResultRead.PipelineNames)
	require.Equal(oldJobResult.OSBuildOutput, oldJobResultRead.OSBuildOutput)
	require.Equal(oldJobResult.HostOS, oldJobResultRead.HostOS)
	require.Equal(oldJobResult.Arch, oldJobResultRead.Arch)

	newJobResult := &worker.OSBuildKojiJobResult{
		HostOS: "rhel-10",
		Arch:   "k",
		PipelineNames: &worker.PipelineNames{
			Build:   []string{"build-result"},
			Payload: []string{"result-test-payload", "result-test-assembler"},
		},
		OSBuildOutput: &osbuild2.Result{
			Type:    "result",
			Success: true,
			Log: map[string]osbuild2.PipelineResult{
				"build-new": {
					osbuild2.StageResult{
						ID:      "---",
						Type:    "org.osbuild.test",
						Output:  "<test output new>",
						Success: true,
					},
				},
			},
		},
	}
	newJobResultRaw, err := json.Marshal(newJobResult)
	require.NoError(err)
	newJobToken := jobTokens[newJobID]
	err = server.FinishJob(newJobToken, newJobResultRaw)
	require.NoError(err)

	newJobResultRead := new(worker.OSBuildKojiJobResult)
	_, _, err = server.JobStatus(newJobID, newJobResultRead)
	require.NoError(err)
	require.Equal(newJobResult, newJobResultRead)
}

func TestDepsolveLegacyErrorConversion(t *testing.T) {
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(t, err)
	defer os.RemoveAll(tempdir)

	distroStruct := test_distro.New()
	arch, err := distroStruct.GetArch(test_distro.TestArchName)
	if err != nil {
		t.Fatalf("error getting arch from distro: %v", err)
	}
	server := newTestServer(t, tempdir, time.Duration(0), "/api/worker/v1")

	depsolveJobId, err := server.EnqueueDepsolve(&worker.DepsolveJob{})
	require.NoError(t, err)

	_, _, _, _, _, err = server.RequestJob(context.Background(), arch.Name(), []string{"depsolve"})
	require.NoError(t, err)

	reason := "Depsolve failed"
	errType := worker.DepsolveErrorType

	expectedResult := worker.DepsolveJobResult{
		Error:     reason,
		ErrorType: errType,
		JobResult: worker.JobResult{
			JobError: clienterrors.WorkerClientError(clienterrors.ErrorDNFDepsolveError, reason),
		},
	}

	depsolveJobResult := worker.DepsolveJobResult{
		Error:     reason,
		ErrorType: errType,
	}

	_, _, err = server.JobStatus(depsolveJobId, &depsolveJobResult)
	require.NoError(t, err)
	require.Equal(t, expectedResult, depsolveJobResult)
}

// Enquueue OSBuild jobs and save both kinds of
// error types (old & new) to the queue and ensure
// that both kinds of errors can be read back
func TestMixedOSBuildJobErrors(t *testing.T) {
	require := require.New(t)
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(err)
	defer os.RemoveAll(tempdir)

	emptyManifestV2 := distro.Manifest(`{"version":"2","pipelines":{}}`)
	server := newTestServer(t, tempdir, time.Millisecond*10, "/")

	oldJob := worker.OSBuildJob{
		Manifest:  emptyManifestV2,
		ImageName: "no-pipeline-names",
	}
	oldJobID, err := server.EnqueueOSBuild("x", &oldJob)
	require.NoError(err)

	newJob := worker.OSBuildJob{
		Manifest:  emptyManifestV2,
		ImageName: "with-pipeline-names",
		PipelineNames: &worker.PipelineNames{
			Build:   []string{"build"},
			Payload: []string{"other", "pipelines"},
		},
	}
	newJobID, err := server.EnqueueOSBuild("x", &newJob)
	require.NoError(err)

	oldJobRead := new(worker.OSBuildJob)
	_, _, _, err = server.Job(oldJobID, oldJobRead)
	require.NoError(err)
	// Not entirely equal
	require.NotEqual(oldJob, oldJobRead)

	// NewJob the same when read back
	newJobRead := new(worker.OSBuildJob)
	_, _, _, err = server.Job(newJobID, newJobRead)
	require.NoError(err)

	// Dequeue the jobs (via RequestJob) to get their tokens and update them to
	// test the result retrieval

	getJob := func() (uuid.UUID, uuid.UUID) {
		// don't block forever if the jobs weren't added or can't be retrieved
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		id, token, _, _, _, err := server.RequestJob(ctx, "x", []string{"osbuild"})
		require.NoError(err)
		return id, token
	}

	getJobTokens := func(n uint) map[uuid.UUID]uuid.UUID {
		tokens := make(map[uuid.UUID]uuid.UUID, n)
		for idx := uint(0); idx < n; idx++ {
			id, token := getJob()
			tokens[id] = token
		}
		return tokens
	}

	jobTokens := getJobTokens(2)
	// make sure we got them both as expected
	require.Contains(jobTokens, oldJobID)
	require.Contains(jobTokens, newJobID)

	oldJobResult := &worker.OSBuildJobResult{
		TargetErrors: []string{"Upload error"},
	}
	oldJobResultRaw, err := json.Marshal(oldJobResult)
	require.NoError(err)
	oldJobToken := jobTokens[oldJobID]
	err = server.FinishJob(oldJobToken, oldJobResultRaw)
	require.NoError(err)

	oldJobResultRead := new(worker.OSBuildJobResult)
	_, _, err = server.JobStatus(oldJobID, oldJobResultRead)
	require.NoError(err)

	require.NotEqual(oldJobResult, oldJobResultRead)
	require.Equal(oldJobResult.Success, false)
	require.Equal(oldJobResultRead.Success, false)

	newJobResult := &worker.OSBuildJobResult{
		PipelineNames: &worker.PipelineNames{
			Build:   []string{"build-result"},
			Payload: []string{"result-test-payload", "result-test-assembler"},
		},
		JobResult: worker.JobResult{
			JobError: clienterrors.WorkerClientError(clienterrors.ErrorUploadingImage, "Error uploading image", nil),
		},
	}
	newJobResultRaw, err := json.Marshal(newJobResult)
	require.NoError(err)
	newJobToken := jobTokens[newJobID]
	err = server.FinishJob(newJobToken, newJobResultRaw)
	require.NoError(err)

	newJobResultRead := new(worker.OSBuildJobResult)
	_, _, err = server.JobStatus(newJobID, newJobResultRead)
	require.NoError(err)
	require.Equal(newJobResult, newJobResultRead)
	require.Equal(newJobResult.Success, false)
	require.Equal(newJobResultRead.Success, false)
}

// Enquueue Koji jobs and save both kinds of
// error types (old & new) to the queue and ensure
// that both kinds of errors can be read back
func TestMixedOSBuildKojiJobErrors(t *testing.T) {
	require := require.New(t)
	tempdir, err := ioutil.TempDir("", "worker-tests-")
	require.NoError(err)
	defer os.RemoveAll(tempdir)

	emptyManifestV2 := distro.Manifest(`{"version":"2","pipelines":{}}`)
	server := newTestServer(t, tempdir, time.Duration(0), "/api/worker/v1")

	enqueueKojiJob := func(job *worker.OSBuildKojiJob) uuid.UUID {
		initJob := new(worker.KojiInitJob)
		initJobID, err := server.EnqueueKojiInit(initJob)
		require.NoError(err)
		jobID, err := server.EnqueueOSBuildKoji("k", job, initJobID)
		require.NoError(err)
		return jobID
	}
	oldJob := worker.OSBuildKojiJob{
		Manifest:  emptyManifestV2,
		ImageName: "no-pipeline-names",
	}
	oldJobID := enqueueKojiJob(&oldJob)

	newJob := worker.OSBuildKojiJob{
		Manifest:  emptyManifestV2,
		ImageName: "with-pipeline-names",
		PipelineNames: &worker.PipelineNames{
			Build:   []string{"build"},
			Payload: []string{"other", "pipelines"},
		},
	}
	newJobID := enqueueKojiJob(&newJob)

	oldJobRead := new(worker.OSBuildKojiJob)
	_, _, _, err = server.Job(oldJobID, oldJobRead)
	require.NoError(err)
	// Not entirely equal
	require.NotEqual(oldJob, oldJobRead)

	// NewJob the same when read back
	newJobRead := new(worker.OSBuildKojiJob)
	_, _, _, err = server.Job(newJobID, newJobRead)
	require.NoError(err)

	// Dequeue the jobs (via RequestJob) to get their tokens and update them to
	// test the result retrieval

	// Finish init jobs
	for idx := uint(0); idx < 2; idx++ {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_, token, _, _, _, err := server.RequestJob(ctx, "k", []string{"koji-init"})
		require.NoError(err)
		require.NoError(server.FinishJob(token, nil))
	}

	getJob := func() (uuid.UUID, uuid.UUID) {
		// don't block forever if the jobs weren't added or can't be retrieved
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		id, token, _, _, _, err := server.RequestJob(ctx, "k", []string{"osbuild-koji"})
		require.NoError(err)
		return id, token
	}

	getJobTokens := func(n uint) map[uuid.UUID]uuid.UUID {
		tokens := make(map[uuid.UUID]uuid.UUID, n)
		for idx := uint(0); idx < n; idx++ {
			id, token := getJob()
			tokens[id] = token
		}
		return tokens
	}

	jobTokens := getJobTokens(2)
	// make sure we got them both as expected
	require.Contains(jobTokens, oldJobID)
	require.Contains(jobTokens, newJobID)

	oldJobResult := &worker.OSBuildKojiJobResult{
		KojiError: "koji build error",
	}
	oldJobResultRaw, err := json.Marshal(oldJobResult)
	require.NoError(err)
	oldJobToken := jobTokens[oldJobID]
	err = server.FinishJob(oldJobToken, oldJobResultRaw)
	require.NoError(err)

	oldJobResultRead := new(worker.OSBuildKojiJobResult)
	_, _, err = server.JobStatus(oldJobID, oldJobResultRead)
	require.NoError(err)

	// oldJobResultRead should have PipelineNames now
	require.NotEqual(oldJobResult, oldJobResultRead)

	newJobResult := &worker.OSBuildKojiJobResult{
		PipelineNames: &worker.PipelineNames{
			Build:   []string{"build-result"},
			Payload: []string{"result-test-payload", "result-test-assembler"},
		},
		JobResult: worker.JobResult{
			JobError: clienterrors.WorkerClientError(clienterrors.ErrorKojiBuild, "Koji build error", nil),
		},
	}
	newJobResultRaw, err := json.Marshal(newJobResult)
	require.NoError(err)
	newJobToken := jobTokens[newJobID]
	err = server.FinishJob(newJobToken, newJobResultRaw)
	require.NoError(err)

	newJobResultRead := new(worker.OSBuildKojiJobResult)
	_, _, err = server.JobStatus(newJobID, newJobResultRead)
	require.NoError(err)
	require.Equal(newJobResult, newJobResultRead)
}
