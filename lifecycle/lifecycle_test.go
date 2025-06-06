package lifecycle

import (
	"encoding/json"
	"fmt"
	artifactoryAuth "github.com/jfrog/jfrog-client-go/artifactory/auth"
	"github.com/jfrog/jfrog-client-go/artifactory/services/utils"
	"github.com/jfrog/jfrog-client-go/http/jfroghttpclient"
	lifecycle "github.com/jfrog/jfrog-client-go/lifecycle/services"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const (
	rbManifestName   = "release-bundle.json.evd"
	releaseBundlesV2 = "release-bundles-v2"
)

var testRb = lifecycle.ReleaseBundleDetails{
	ReleaseBundleName:    "bundle-test",
	ReleaseBundleVersion: "1.2.3",
}

type testCase struct {
	sync        bool
	errExpected bool
	finalStatus lifecycle.RbStatus
}

func TestSimpleGetReleaseBundleStatus(t *testing.T) {
	testCases := map[string]testCase{
		"no sync processing": {sync: false, errExpected: false, finalStatus: lifecycle.Processing},
		"no sync pending":    {sync: false, errExpected: false, finalStatus: lifecycle.Pending},
		"no sync completed":  {sync: false, errExpected: false, finalStatus: lifecycle.Completed},
		"no sync failed":     {sync: false, errExpected: false, finalStatus: lifecycle.Failed},
		"sync completed":     {sync: true, errExpected: false, finalStatus: lifecycle.Completed},
		"sync rejected":      {sync: true, errExpected: false, finalStatus: lifecycle.Rejected},
		"sync failed":        {sync: true, errExpected: false, finalStatus: lifecycle.Failed},
		"sync deleting":      {sync: true, errExpected: false, finalStatus: lifecycle.Deleting},
		"unexpected status":  {sync: true, errExpected: true, finalStatus: "some status"},
	}
	for testName, test := range testCases {
		t.Run(testName, func(t *testing.T) {
			handlerFunc, requestNum := createDefaultHandlerFunc(t, test.finalStatus)
			testGetRBStatus(t, test, handlerFunc)
			assert.Equal(t, 1, *requestNum)
		})
	}

}

func TestComplexReleaseBundleWaitForOperation(t *testing.T) {
	lifecycle.SyncSleepInterval = 1 * time.Second
	defer func() { lifecycle.SyncSleepInterval = lifecycle.DefaultSyncSleepInterval }()

	requestNum := 0
	handlerFunc := func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "/"+lifecycle.GetReleaseBundleCreationStatusRestApi(testRb) {
			w.WriteHeader(http.StatusOK)
			var rbStatus lifecycle.RbStatus
			switch requestNum {
			case 0:
				rbStatus = lifecycle.Pending
			case 1:
				rbStatus = lifecycle.Processing
			case 2:
				rbStatus = lifecycle.Completed
			}
			requestNum++
			writeMockStatusResponse(t, w, lifecycle.ReleaseBundleStatusResponse{Status: rbStatus})
		}
	}
	test := testCase{sync: true, errExpected: false, finalStatus: lifecycle.Completed}
	testGetRBStatus(t, test, handlerFunc)
	assert.Equal(t, 3, requestNum)
}

func testGetRBStatus(t *testing.T, test testCase, handlerFunc http.HandlerFunc) {
	mockServer, rbService := createMockServer(t, handlerFunc)
	defer mockServer.Close()

	statusResp, err := rbService.GetReleaseBundleCreationStatus(testRb, "", test.sync)
	if test.errExpected {
		assert.Error(t, err)
		return
	}

	assert.NoError(t, err)
	assert.Equal(t, test.finalStatus, statusResp.Status)
}

func TestGetReleaseBundleSpecArtifactsOnly(t *testing.T) {
	mockServer, rbService := createMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "/"+lifecycle.GetReleaseBundleSpecificationRestApi(testRb) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{
				"schema_version": "1.0.0",
				"service_id": "jfrt@01h0nvs1pwjtzs15x7kbtv1sve",
				"created_by": "admin",
				"created": "2023-05-18T11:26:02.912Z",
				"created_millis": 1684409162912,
				"artifacts": [
					{
						"path": "catalina/release-notes-1.0.0.txt",
						"checksum": "e06f59f5a976c7f4a5406907790bb8cad6148406282f07cd143fd1de64ca169d",
						"source_repository_key": "catalina-dev-generic-local",
						"package_type": "generic",
						"size": 470,
						"properties": [
							{
								"key": "build.name",
								"values": [
									"Catalina-Build"
								]
							}
						]
					}
				],
				"checked_webhooks": [],
				"source": {
					"aql": "{source-AQL}",
					"builds": [
						{
							"build_repository": "artifactory-build-info",
							"build_name": "Commons-Build",
							"build_number": "1.0.1",
							"build_started": "2023-04-05T07:00:00.000+0200",
							"include_dependencies": false
						}
					],
					"release_bundles": [
						{
							"project_key": "default",
							"repository_key": "release-bundles-v2",
							"release_bundle_name": "Commons-Bundle",
							"release_bundle_version": "1.0.0"
						}
					]
				}
			}`))
			assert.NoError(t, err)
		}
	})
	defer mockServer.Close()

	specResp, err := rbService.GetReleaseBundleSpecification(testRb)
	assert.NoError(t, err)
	assert.Equal(t, "admin", specResp.CreatedBy)
	assert.Equal(t, "2023-05-18T11:26:02Z", specResp.Created.Format(time.RFC3339))
	assert.Equal(t, 1684409162912, specResp.CreatedMillis)

	assert.Len(t, specResp.Artifacts, 1)
	assert.Equal(t, "catalina/release-notes-1.0.0.txt", specResp.Artifacts[0].Path)
	assert.Equal(t, "generic", specResp.Artifacts[0].PackageType)
	assert.Equal(t, "catalina-dev-generic-local", specResp.Artifacts[0].SourceRepositoryKey)
	assert.Equal(t, 470, specResp.Artifacts[0].Size)
	assert.Len(t, specResp.Artifacts[0].Properties, 1)
	assert.Equal(t, "build.name", specResp.Artifacts[0].Properties[0].Key)
	assert.Equal(t, []string{"Catalina-Build"}, specResp.Artifacts[0].Properties[0].Values)
}

func createMockServer(t *testing.T, testHandler http.HandlerFunc) (*httptest.Server, *lifecycle.ReleaseBundlesService) {
	testServer := httptest.NewServer(testHandler)

	rtDetails := artifactoryAuth.NewArtifactoryDetails()
	rtDetails.SetUrl(testServer.URL + "/")

	client, err := jfroghttpclient.JfrogClientBuilder().Build()
	assert.NoError(t, err)
	return testServer, lifecycle.NewReleaseBundlesService(rtDetails, client)
}

func writeMockStatusResponse(t *testing.T, w http.ResponseWriter, resp interface{}) {
	content, err := json.Marshal(resp)
	assert.NoError(t, err)
	_, err = w.Write(content)
	assert.NoError(t, err)
}

func createDefaultHandlerFunc(t *testing.T, status lifecycle.RbStatus) (http.HandlerFunc, *int) {
	requestNum := 0
	return func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "/"+lifecycle.GetReleaseBundleCreationStatusRestApi(testRb) {
			w.WriteHeader(http.StatusOK)
			requestNum++
			writeMockStatusResponse(t, w, lifecycle.ReleaseBundleStatusResponse{Status: status})
		}
	}, &requestNum
}

func TestRemoteDeleteReleaseBundle(t *testing.T) {
	lifecycle.SyncSleepInterval = 1 * time.Second
	defer func() { lifecycle.SyncSleepInterval = lifecycle.DefaultSyncSleepInterval }()

	requestNum := 0
	handlerFunc := func(w http.ResponseWriter, r *http.Request) {
		switch r.RequestURI {
		case "/" + lifecycle.GetReleaseBundleDistributionsApi(testRb):
			w.WriteHeader(http.StatusOK)
			var rbStatus lifecycle.RbStatus
			switch requestNum {
			case 0:
				rbStatus = lifecycle.InProgress
			case 1:
				rbStatus = lifecycle.InProgress
			case 2:
				rbStatus = lifecycle.Completed
			}
			requestNum++
			writeMockStatusResponse(t, w, lifecycle.GetDistributionsResponse{{Status: rbStatus}})
		case "/" + lifecycle.GetRemoteDeleteReleaseBundleApi(testRb):
			w.WriteHeader(http.StatusAccepted)
		}
	}

	mockServer, rbService := createMockServer(t, handlerFunc)
	defer mockServer.Close()

	assert.NoError(t, rbService.RemoteDeleteReleaseBundle(testRb, lifecycle.ReleaseBundleRemoteDeleteParams{MaxWaitMinutes: 2}))
}

func TestGetReleaseBundleVersionPromotions(t *testing.T) {
	mockServer, rbService := createMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "/"+lifecycle.GetGetReleaseBundleVersionPromotionsApi(testRb) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{
    "promotions": [
        {
            "status": "COMPLETED",
            "repository_key": "release-bundles-v2",
            "release_bundle_name": "bundle-test",
            "release_bundle_version": "1.2.3",
            "environment": "PROD",
            "service_id": "jfrt@012345r6315rxa03z99nec1zns",
            "created_by": "admin",
            "created": "2024-03-14T15:26:46.637Z",
            "created_millis": 1710430006637
        }
    ]
}`))
			assert.NoError(t, err)
		}
	})
	defer mockServer.Close()

	resp, err := rbService.GetReleaseBundleVersionPromotions(testRb, lifecycle.GetPromotionsOptionalQueryParams{})
	assert.NoError(t, err)
	if !assert.Len(t, resp.Promotions, 1) {
		return
	}
	promotion := resp.Promotions[0]
	assert.Equal(t, lifecycle.Completed, promotion.Status)
	assert.Equal(t, "release-bundles-v2", promotion.RepositoryKey)
	assert.Equal(t, testRb.ReleaseBundleName, promotion.ReleaseBundleName)
	assert.Equal(t, testRb.ReleaseBundleVersion, promotion.ReleaseBundleVersion)
	assert.Equal(t, "PROD", promotion.Environment)
	assert.Equal(t, "jfrt@012345r6315rxa03z99nec1zns", promotion.ServiceId)
	assert.Equal(t, "admin", promotion.CreatedBy)
	assert.Equal(t, "2024-03-14T15:26:46.637Z", promotion.Created)
	assert.Equal(t, "1710430006637", promotion.CreatedMillis.String())
}

func TestIsReleaseBundleExist(t *testing.T) {
	mockServer, rbService := createMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "/"+lifecycle.GetIsExistReleaseBundleApi("rbName/reVersion") {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(
				`{"exists":true}`))
			assert.NoError(t, err)
		}
	})
	defer mockServer.Close()
	exist, err := rbService.ReleaseBundleExists("rbName", "reVersion", "")
	assert.NoError(t, err)
	assert.True(t, exist)
}

func TestIsReleaseBundleExistWithProject(t *testing.T) {
	mockServer, rbService := createMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "/"+lifecycle.GetIsExistReleaseBundleApi("rbName/reVersion?project=projectKey") {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(
				`{"exists":false}`))
			assert.NoError(t, err)
		}
	})
	defer mockServer.Close()
	exist, err := rbService.ReleaseBundleExists("rbName", "reVersion", "projectKey")
	assert.NoError(t, err)
	assert.False(t, exist)
}

func TestReleaseBundleAnnotate(t *testing.T) {
	mockServer, rbService := createMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "/"+lifecycle.GetReleaseBundleSetTagApi(testRb) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{
        {
            "repository_key": "release-bundles-v2",
            "release_bundle_name": "bundle-test",
            "release_bundle_version": "1.2.3",
			"release_bundle_tag" : "bundle-tag"
        }
}`))
			assert.NoError(t, err)
		}
		if r.RequestURI == "/artifactory/"+lifecycle.PropertiesBaseApi {
			w.WriteHeader(http.StatusOK) // Status 201
			writeMockStatusResponse(t, w, map[string]interface{}{"message": "Created", "status": "201"})
		}
	})
	defer mockServer.Close()
	properties := "environment=qa335;buildNumber=335"
	annotateOperationParams := buildAnnotationOperationParams(testRb, "default", "bundle-tag", properties,
		"environment", mockServer.URL)
	err := rbService.AnnotateReleaseBundle(annotateOperationParams)
	assert.NoError(t, err)
}

func testAnnotate(t *testing.T, projectKey, tag, properties, delProperties string, expectError bool) {
	mockServer, rbService := createMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "/"+lifecycle.GetReleaseBundleSetTagApi(testRb) {
			w.WriteHeader(http.StatusOK)
		}
		if r.RequestURI == "/artifactory/"+lifecycle.PropertiesBaseApi {
			w.WriteHeader(http.StatusNoContent) // Status 204
			writeMockStatusResponse(t, w, map[string]interface{}{"message": "Created", "status": "201"})
		}
		if r.RequestURI == "/artifactory/"+lifecycle.PropertiesBaseApi {
			w.WriteHeader(http.StatusNoContent) // Status 200
		}
	})
	defer mockServer.Close()
	annotateOperationParams := buildAnnotationOperationParams(testRb, projectKey, tag, properties, delProperties,
		mockServer.URL)
	err := rbService.AnnotateReleaseBundle(annotateOperationParams)
	if expectError {
		assert.Error(t, err)
		return
	}
	assert.NoError(t, err)
}

func TestReleaseBundleAnnotationCases(t *testing.T) {
	testCases := []struct {
		name          string
		projectKey    string
		tag           string
		properties    string
		delProperties string
		expectError   bool
	}{
		{"tag, properties, del-prop are not empty, project are empty", "", "bundle-tag", "prop1=1;prop2=2", "prop1", false},
		{"tag, property, del-prop are not empty, project is default", "default", "bundle-tag", "prop1=1;prop2=2", "prop1", false},
		{"tag is empty, del-prop is empty", "default", "", "prop1=1;prop2=2", "", false},
		{"property is empty, del-prop is empty, tag isn't empty, project is default", "default", "bundle-tag", "", "", false},
		{"property is empty", "project", "bundle-tag", "", "", false},
		{"property is one pair, tag isn't empty", "project", "bundle-tag", "prop1=1", "prop1", false},
		{"property is one pair, tag is empty", "project", "", "prop1=1", "", false},
	}

	for _, test := range testCases {
		t.Run(test.name, func(t *testing.T) {
			testAnnotate(t, test.projectKey, test.tag, test.properties, test.delProperties, test.expectError)
		})
	}
}

func buildAnnotationOperationParams(rbDetails lifecycle.ReleaseBundleDetails, projectKey, bundleTag, properties, delProperties,
	artUrl string) lifecycle.AnnotateOperationParams {
	props, _ := utils.ParseProperties(properties)
	annotateOperationParams := lifecycle.AnnotateOperationParams{
		RbTag: lifecycle.RbAnnotationTag{
			Tag:   bundleTag,
			Exist: true,
		},
		RbProps: lifecycle.RbAnnotationProps{
			Properties: props.ToMap(),
			Exist:      true,
		},
		RbDetails: rbDetails,
		QueryParams: lifecycle.CommonOptionalQueryParams{
			ProjectKey: projectKey,
		},
		PropertyParams: lifecycle.CommonPropParams{
			Path:      resolveManifestPath(projectKey, rbDetails.ReleaseBundleName, rbDetails.ReleaseBundleVersion),
			Recursive: false,
		},
		RbDelProps: lifecycle.RbDelProps{
			Keys:  delProperties,
			Exist: true,
		},
		ArtifactoryUrl: lifecycle.ArtCommonParams{
			Url: artUrl + "/artifactory/",
		},
	}
	return annotateOperationParams
}

func resolveManifestPath(projectKey, bundleName, bundleVersion string) string {
	return fmt.Sprintf("%s/%s/%s/%s", resolveRepoKey(projectKey), bundleName, bundleVersion, rbManifestName)
}
func resolveRepoKey(project string) string {
	if project == "" || project == "default" {
		return releaseBundlesV2
	}
	return fmt.Sprintf("%s-%s", project, releaseBundlesV2)
}
