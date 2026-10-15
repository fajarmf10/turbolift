/*
 * Copyright 2026 Fajar Maulana Firdaus.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * https://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package github

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/skyscanner/turbolift/internal/executor"
	"github.com/skyscanner/turbolift/internal/platform"
)

const (
	originURL   = "git@gitlab.com:group/repo1.git"
	forkURL     = "git@gitlab.com:me/repo1.git"
	mrListJSON  = `[{"iid":7,"project_id":99,"source_project_id":99,"state":"opened","source_branch":"turbolift-campaign"}]`
	currentUser = `{"id":42,"username":"me"}`
	mrViewJSON  = `{"iid":7,"project_id":99,"title":"some title","state":"opened","source_branch":"turbolift-campaign","web_url":"https://gitlab.com/group/repo1/-/merge_requests/7","has_conflicts":false,"detailed_merge_status":"mergeable","upvotes":2,"downvotes":0,"head_pipeline":{"status":"success"}}`
)

var originProjectID = canned{output: "99\n"}

type canned struct {
	output string
	err    error
}

var noUpstream = canned{"error: No such remote 'upstream'", errors.New("exit status 2")}

func newCannedExecutor(responses map[string]canned) *executor.FakeExecutor {
	respond := func(name string, args ...string) (string, error) {
		response := responses[strings.Join(append([]string{name}, args...), " ")]
		return response.output, response.err
	}
	return executor.NewFakeExecutor(func(_ string, name string, args ...string) error {
		_, err := respond(name, args...)
		return err
	}, func(_ string, name string, args ...string) (string, error) {
		return respond(name, args...)
	})
}

func TestItUsesGitLabWhenThePlatformIsGitLab(t *testing.T) {
	t.Setenv(platform.EnvVar, "gitlab")
	assert.IsType(t, &RealGitLab{}, NewRealGitHub())

	t.Setenv(platform.EnvVar, "")
	assert.IsType(t, &RealGitHub{}, NewRealGitHub())
}

func project(id int, pathWithNamespace string, importStatus string) string {
	return fmt.Sprintf(`{"id":%d,"path_with_namespace":%q,"default_branch":"main","ssh_url_to_repo":"git@gitlab.com:%s.git","http_url_to_repo":"https://gitlab.com/%s.git","import_status":%q}`, id, pathWithNamespace, pathWithNamespace, pathWithNamespace, importStatus)
}

func TestGitLabClonesTheProjectOverSSHIntoTheRepoNameDirectory(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api projects/group%2Fsubgroup%2Frepo1":     {output: project(99, "group/subgroup/repo1", "finished")},
		"glab config get host":                           {output: "gitlab.com\n"},
		"glab config get git_protocol --host gitlab.com": {output: "ssh\n"},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().Clone(&strings.Builder{}, "work/group/subgroup", "group/subgroup/repo1")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/subgroup", "glab", "api", "projects/group%2Fsubgroup%2Frepo1"},
		{"work/group/subgroup", "glab", "config", "get", "host"},
		{"work/group/subgroup", "glab", "config", "get", "git_protocol", "--host", "gitlab.com"},
		{"work/group/subgroup", "git", "clone", "git@gitlab.com:group/subgroup/repo1.git", "repo1"},
	})
}

func TestGitLabClonesOverHTTPSWhenGlabIsConfiguredForIt(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api projects/group%2Frepo1":                {output: project(99, "group/repo1", "finished")},
		"glab config get host":                           {output: "gitlab.com\n"},
		"glab config get git_protocol --host gitlab.com": {output: "https\n"},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().Clone(&strings.Builder{}, "work/group", "group/repo1")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group", "glab", "api", "projects/group%2Frepo1"},
		{"work/group", "glab", "config", "get", "host"},
		{"work/group", "glab", "config", "get", "git_protocol", "--host", "gitlab.com"},
		{"work/group", "git", "clone", "https://gitlab.com/group/repo1.git", "repo1"},
	})
}

func TestGitLabClonesAHostPrefixedRepoFromThatHost(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api projects/group%2Frepo1 --hostname gitlab.example.com": {output: project(99, "group/repo1", "finished")},
		"glab config get git_protocol --host gitlab.example.com":        {output: "ssh\n"},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().Clone(&strings.Builder{}, "work/group", "gitlab.example.com/group/repo1")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group", "glab", "api", "projects/group%2Frepo1", "--hostname", "gitlab.example.com"},
		{"work/group", "glab", "config", "get", "git_protocol", "--host", "gitlab.example.com"},
		{"work/group", "git", "clone", "git@gitlab.com:group/repo1.git", "repo1"},
	})
}

func TestGitLabReturnsErrorOnFailedClone(t *testing.T) {
	execInstance = executor.NewAlwaysFailsFakeExecutor()

	err := NewRealGitLab().Clone(&strings.Builder{}, "work/group", "group/repo1")
	assert.Error(t, err)
}

func TestGitLabCreatesAForkWaitsForItAndClonesItWithAnUpstreamRemote(t *testing.T) {
	forkImportPollInterval = 0
	t.Cleanup(func() { forkImportPollInterval = 2 * time.Second })
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api user": {output: currentUser},
		"glab api projects/group%2Fsubgroup%2Frepo1":     {output: project(99, "group/subgroup/repo1", "finished")},
		"glab api projects/99/forks?owned=true":          {output: "[]"},
		"glab api projects/99/fork -X POST":              {output: project(42, "me/repo1", "scheduled")},
		"glab api projects/42":                           {output: project(42, "me/repo1", "finished")},
		"glab config get host":                           {output: "gitlab.com\n"},
		"glab config get git_protocol --host gitlab.com": {output: "ssh\n"},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().ForkAndClone(&strings.Builder{}, "work/group/subgroup", "group/subgroup/repo1")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/subgroup", "glab", "api", "user"},
		{"work/group/subgroup", "glab", "api", "projects/group%2Fsubgroup%2Frepo1"},
		{"work/group/subgroup", "glab", "api", "projects/99/forks?owned=true"},
		{"work/group/subgroup", "glab", "api", "projects/99/fork", "-X", "POST"},
		{"work/group/subgroup", "glab", "api", "projects/42"},
		{"work/group/subgroup", "glab", "config", "get", "host"},
		{"work/group/subgroup", "glab", "config", "get", "git_protocol", "--host", "gitlab.com"},
		{"work/group/subgroup", "git", "clone", "git@gitlab.com:me/repo1.git", "repo1"},
		{"work/group/subgroup/repo1", "git", "remote", "add", "upstream", "git@gitlab.com:group/subgroup/repo1.git"},
	})
}

func TestGitLabReusesTheExistingForkOfTheSameProject(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api user":                                  {output: currentUser},
		"glab api projects/group%2Frepo1":                {output: project(99, "group/repo1", "finished")},
		"glab api projects/99/forks?owned=true":          {output: "[" + project(7, "some-group/repo1", "finished") + "," + project(42, "me/repo1-renamed", "finished") + "]"},
		"glab config get host":                           {output: "gitlab.com\n"},
		"glab config get git_protocol --host gitlab.com": {output: "ssh\n"},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().ForkAndClone(&strings.Builder{}, "work/group", "group/repo1")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group", "glab", "api", "user"},
		{"work/group", "glab", "api", "projects/group%2Frepo1"},
		{"work/group", "glab", "api", "projects/99/forks?owned=true"},
		{"work/group", "glab", "config", "get", "host"},
		{"work/group", "glab", "config", "get", "git_protocol", "--host", "gitlab.com"},
		{"work/group", "git", "clone", "git@gitlab.com:me/repo1-renamed.git", "repo1"},
		{"work/group/repo1", "git", "remote", "add", "upstream", "git@gitlab.com:group/repo1.git"},
	})
}

func TestGitLabDoesNotCloneWhenTheForkCannotBeCreated(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api user":                         {output: currentUser},
		"glab api projects/group%2Frepo1":       {output: project(99, "group/repo1", "finished")},
		"glab api projects/99/forks?owned=true": {output: "[]"},
		"glab api projects/99/fork -X POST":     {output: `glab: Name has already been taken (HTTP 409)`, err: errors.New("exit status 1")},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().ForkAndClone(&strings.Builder{}, "work/group", "group/repo1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "group/repo1")

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group", "glab", "api", "user"},
		{"work/group", "glab", "api", "projects/group%2Frepo1"},
		{"work/group", "glab", "api", "projects/99/forks?owned=true"},
		{"work/group", "glab", "api", "projects/99/fork", "-X", "POST"},
	})
}

func TestGitLabDoesNotCloneAForkThatFailedToImport(t *testing.T) {
	execInstance = newCannedExecutor(map[string]canned{
		"glab api user":                         {output: currentUser},
		"glab api projects/group%2Frepo1":       {output: project(99, "group/repo1", "finished")},
		"glab api projects/99/forks?owned=true": {output: "[]"},
		"glab api projects/99/fork -X POST":     {output: project(42, "me/repo1", "failed")},
	})

	err := NewRealGitLab().ForkAndClone(&strings.Builder{}, "work/group", "group/repo1")
	assert.Error(t, err)
}

func TestGitLabForksOnASelfManagedHost(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api user --hostname gitlab.example.com":                         {output: currentUser},
		"glab api projects/group%2Frepo1 --hostname gitlab.example.com":       {output: project(99, "group/repo1", "finished")},
		"glab api projects/99/forks?owned=true --hostname gitlab.example.com": {output: "[" + project(42, "me/repo1", "finished") + "]"},
		"glab config get git_protocol --host gitlab.example.com":              {output: "ssh\n"},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().ForkAndClone(&strings.Builder{}, "work/group", "gitlab.example.com/group/repo1")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group", "glab", "api", "user", "--hostname", "gitlab.example.com"},
		{"work/group", "glab", "api", "projects/group%2Frepo1", "--hostname", "gitlab.example.com"},
		{"work/group", "glab", "api", "projects/99/forks?owned=true", "--hostname", "gitlab.example.com"},
		{"work/group", "glab", "config", "get", "git_protocol", "--host", "gitlab.example.com"},
		{"work/group", "git", "clone", "git@gitlab.com:me/repo1.git", "repo1"},
		{"work/group/repo1", "git", "remote", "add", "upstream", "git@gitlab.com:group/repo1.git"},
	})
}

func TestGitLabCanPushWithDeveloperAccessFromAParentGroup(t *testing.T) {
	cwd, _ := os.Getwd()
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api user": {output: currentUser},
		"glab api projects/group%2Fsubgroup%2Frepo1/members/all/42": {output: `{"username":"me","access_level":30}`},
	})
	execInstance = fakeExecutor

	pushable, err := NewRealGitLab().IsPushable(&strings.Builder{}, "group/subgroup/repo1")
	assert.NoError(t, err)
	assert.True(t, pushable)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{cwd, "glab", "api", "user"},
		{cwd, "glab", "api", "projects/group%2Fsubgroup%2Frepo1/members/all/42"},
	})
}

func TestGitLabCannotPushWithReporterAccess(t *testing.T) {
	execInstance = newCannedExecutor(map[string]canned{
		"glab api user": {output: currentUser},
		"glab api projects/group%2Frepo1/members/all/42": {output: `{"username":"me","access_level":20}`},
	})

	pushable, err := NewRealGitLab().IsPushable(&strings.Builder{}, "group/repo1")
	assert.NoError(t, err)
	assert.False(t, pushable)
}

func TestGitLabCannotPushWhenTheUserIsNotAMember(t *testing.T) {
	execInstance = newCannedExecutor(map[string]canned{
		"glab api user": {output: currentUser},
		"glab api projects/group%2Frepo1/members/all/42": {output: "glab: 404 Not found (HTTP 404)", err: errors.New("exit status 1")},
	})

	pushable, err := NewRealGitLab().IsPushable(&strings.Builder{}, "group/repo1")
	assert.NoError(t, err)
	assert.False(t, pushable)
}

func TestGitLabReturnsErrorWhenTheMembershipCheckFails(t *testing.T) {
	execInstance = newCannedExecutor(map[string]canned{
		"glab api user": {output: currentUser},
		"glab api projects/group%2Frepo1/members/all/42": {output: "glab: 401 Unauthorized (HTTP 401)", err: errors.New("exit status 1")},
	})

	_, err := NewRealGitLab().IsPushable(&strings.Builder{}, "group/repo1")
	assert.Error(t, err)
}

func TestGitLabLooksUpTheCurrentUserOnlyOnce(t *testing.T) {
	cwd, _ := os.Getwd()
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api user": {output: currentUser},
		"glab api projects/group%2Frepo1/members/all/42": {output: `{"access_level":40}`},
		"glab api projects/group%2Frepo2/members/all/42": {output: `{"access_level":40}`},
	})
	execInstance = fakeExecutor

	gl := NewRealGitLab()
	_, _ = gl.IsPushable(&strings.Builder{}, "group/repo1")
	_, _ = gl.IsPushable(&strings.Builder{}, "group/repo2")

	fakeExecutor.AssertCalledWith(t, [][]string{
		{cwd, "glab", "api", "user"},
		{cwd, "glab", "api", "projects/group%2Frepo1/members/all/42"},
		{cwd, "glab", "api", "projects/group%2Frepo2/members/all/42"},
	})
}

func TestGitLabReadsTheDefaultBranch(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api projects/group%2Frepo1": {output: project(99, "group/repo1", "finished")},
	})
	execInstance = fakeExecutor

	branch, err := NewRealGitLab().GetDefaultBranchName(&strings.Builder{}, "work/group/repo1", "group/repo1")
	assert.NoError(t, err)
	assert.Equal(t, "main", branch)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/repo1", "glab", "api", "projects/group%2Frepo1"},
	})
}

func TestGitLabReadsTheDefaultBranchOfAHostPrefixedRepoFromThatHost(t *testing.T) {
	execInstance = newCannedExecutor(map[string]canned{
		"glab api projects/group%2Frepo1 --hostname gitlab.example.com": {output: project(99, "group/repo1", "finished")},
	})

	branch, err := NewRealGitLab().GetDefaultBranchName(&strings.Builder{}, "work/group/repo1", "gitlab.example.com/group/repo1")
	assert.NoError(t, err)
	assert.Equal(t, "main", branch)
}

func createMergeRequestResponses(commitsAhead string) map[string]canned {
	return map[string]canned{
		"glab api projects/group%2Frepo1":       {output: project(99, "group/repo1", "finished")},
		"git remote get-url upstream":           noUpstream,
		"git remote get-url origin":             {output: originURL + "\n"},
		"git rev-list --count FETCH_HEAD..HEAD": {output: commitsAhead + "\n"},
	}
}

func TestGitLabSkipsTheMergeRequestWhenTheBranchHasNoNewCommits(t *testing.T) {
	fakeExecutor := newCannedExecutor(createMergeRequestResponses("0"))
	execInstance = fakeExecutor

	didCreate, err := NewRealGitLab().CreatePullRequest(&strings.Builder{}, "work/group/repo1", PullRequest{
		Title:        "some title",
		Body:         "some body",
		UpstreamRepo: "group/repo1",
	})
	assert.NoError(t, err)
	assert.False(t, didCreate)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/repo1", "glab", "api", "projects/group%2Frepo1"},
		{"work/group/repo1", "git", "remote", "get-url", "upstream"},
		{"work/group/repo1", "git", "remote", "get-url", "origin"},
		{"work/group/repo1", "git", "fetch", "origin", "main"},
		{"work/group/repo1", "git", "rev-list", "--count", "FETCH_HEAD..HEAD"},
	})
}

func TestGitLabCreatesAMergeRequestIntoTheDefaultBranch(t *testing.T) {
	fakeExecutor := newCannedExecutor(createMergeRequestResponses("2"))
	execInstance = fakeExecutor

	didCreate, err := NewRealGitLab().CreatePullRequest(&strings.Builder{}, "work/group/repo1", PullRequest{
		Title:        "some title",
		Body:         "some body",
		UpstreamRepo: "group/repo1",
	})
	assert.NoError(t, err)
	assert.True(t, didCreate)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/repo1", "glab", "api", "projects/group%2Frepo1"},
		{"work/group/repo1", "git", "remote", "get-url", "upstream"},
		{"work/group/repo1", "git", "remote", "get-url", "origin"},
		{"work/group/repo1", "git", "fetch", "origin", "main"},
		{"work/group/repo1", "git", "rev-list", "--count", "FETCH_HEAD..HEAD"},
		{"work/group/repo1", "glab", "mr", "create", "--title", "some title", "--description", "some body", "--repo", "group/repo1", "--head", originURL, "--target-branch", "main", "--yes"},
	})
}

func TestGitLabCreatesADraftMergeRequest(t *testing.T) {
	fakeExecutor := newCannedExecutor(createMergeRequestResponses("1"))
	execInstance = fakeExecutor

	didCreate, err := NewRealGitLab().CreatePullRequest(&strings.Builder{}, "work/group/repo1", PullRequest{
		Title:        "some title",
		Body:         "some body",
		UpstreamRepo: "group/repo1",
		IsDraft:      true,
	})
	assert.NoError(t, err)
	assert.True(t, didCreate)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/repo1", "glab", "api", "projects/group%2Frepo1"},
		{"work/group/repo1", "git", "remote", "get-url", "upstream"},
		{"work/group/repo1", "git", "remote", "get-url", "origin"},
		{"work/group/repo1", "git", "fetch", "origin", "main"},
		{"work/group/repo1", "git", "rev-list", "--count", "FETCH_HEAD..HEAD"},
		{"work/group/repo1", "glab", "mr", "create", "--title", "some title", "--description", "some body", "--repo", "group/repo1", "--head", originURL, "--target-branch", "main", "--yes", "--draft"},
	})
}

func TestGitLabCreatesAMergeRequestFromAFork(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api projects/group%2Frepo1":       {output: project(99, "group/repo1", "finished")},
		"git remote get-url upstream":           {output: originURL + "\n"},
		"git remote get-url origin":             {output: forkURL + "\n"},
		"git rev-list --count FETCH_HEAD..HEAD": {output: "1\n"},
	})
	execInstance = fakeExecutor

	didCreate, err := NewRealGitLab().CreatePullRequest(&strings.Builder{}, "work/group/repo1", PullRequest{
		Title:        "some title",
		Body:         "some body",
		UpstreamRepo: "group/repo1",
	})
	assert.NoError(t, err)
	assert.True(t, didCreate)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/repo1", "glab", "api", "projects/group%2Frepo1"},
		{"work/group/repo1", "git", "remote", "get-url", "upstream"},
		{"work/group/repo1", "git", "remote", "get-url", "origin"},
		{"work/group/repo1", "git", "fetch", "upstream", "main"},
		{"work/group/repo1", "git", "rev-list", "--count", "FETCH_HEAD..HEAD"},
		{"work/group/repo1", "glab", "mr", "create", "--title", "some title", "--description", "some body", "--repo", "group/repo1", "--head", forkURL, "--target-branch", "main", "--yes"},
	})
}

func TestGitLabCreatesAMergeRequestInAListedRepoThatIsItselfAFork(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api projects/me%2Frepo1":          {output: strings.Replace(project(42, "me/repo1", "finished"), `"main"`, `"fork-main"`, 1)},
		"git remote get-url upstream":           {output: originURL + "\n"},
		"git remote get-url origin":             {output: forkURL + "\n"},
		"git rev-list --count FETCH_HEAD..HEAD": {output: "1\n"},
	})
	execInstance = fakeExecutor

	didCreate, err := NewRealGitLab().CreatePullRequest(&strings.Builder{}, "work/me/repo1", PullRequest{
		Title:        "some title",
		Body:         "some body",
		UpstreamRepo: "me/repo1",
	})
	assert.NoError(t, err)
	assert.True(t, didCreate)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/me/repo1", "glab", "api", "projects/me%2Frepo1"},
		{"work/me/repo1", "git", "remote", "get-url", "upstream"},
		{"work/me/repo1", "git", "remote", "get-url", "origin"},
		{"work/me/repo1", "git", "fetch", "origin", "fork-main"},
		{"work/me/repo1", "git", "rev-list", "--count", "FETCH_HEAD..HEAD"},
		{"work/me/repo1", "glab", "mr", "create", "--title", "some title", "--description", "some body", "--repo", "me/repo1", "--head", forkURL, "--target-branch", "fork-main", "--yes"},
	})
}

func TestGitLabReadsTheProjectPathOfARemote(t *testing.T) {
	for remoteURL, expected := range map[string]string{
		"git@gitlab.com:group/sub/repo1.git":            "group/sub/repo1",
		"ssh://git@gitlab.example.com:2222/group/repo1": "group/repo1",
		"https://gitlab.com/group/repo1.git":            "group/repo1",
	} {
		assert.Equal(t, expected, remoteProjectPath(remoteURL), remoteURL)
	}
}

func TestGitLabReturnsErrorOnFailedCreateMergeRequest(t *testing.T) {
	responses := createMergeRequestResponses("1")
	responses["glab mr create --title some title --description some body --repo group/repo1 --head "+originURL+" --target-branch main --yes"] = canned{err: errors.New("synthetic error")}
	execInstance = newCannedExecutor(responses)

	didCreate, err := NewRealGitLab().CreatePullRequest(&strings.Builder{}, "work/group/repo1", PullRequest{
		Title:        "some title",
		Body:         "some body",
		UpstreamRepo: "group/repo1",
	})
	assert.Error(t, err)
	assert.False(t, didCreate)
}

func TestGitLabReturnsTheMergeRequestStatus(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"git remote get-url upstream":                       noUpstream,
		"git remote get-url origin":                         {output: originURL + "\n"},
		"glab repo view " + originURL + " -F json --jq .id": originProjectID,
		"glab api projects/group%2Frepo1/merge_requests?source_branch=turbolift-campaign&source_project_id=99&state=all": {output: mrListJSON},
		"glab mr view 7 --repo " + originURL + " -F json":                                                                {output: mrViewJSON},
		"glab api projects/99/merge_requests/7/approvals":                                                                {output: `{"approved":true,"approved_by":[{"user":{"username":"reviewer"}}]}`},
	})
	execInstance = fakeExecutor

	status, err := NewRealGitLab().GetPR(&strings.Builder{}, "work/group/repo1", "turbolift-campaign")
	assert.NoError(t, err)
	assert.Equal(t, &PrStatus{
		Closed:            false,
		HeadRefName:       "turbolift-campaign",
		Mergeable:         "MERGEABLE",
		Number:            7,
		ReactionGroups:    []ReactionGroup{{Content: "THUMBS_UP", Users: ReactionGroupUsers{TotalCount: 2}}},
		ReviewDecision:    "APPROVED",
		State:             "OPEN",
		StatusCheckRollup: []StatusCheckRollup{{State: "SUCCESS"}},
		Title:             "some title",
		Url:               "https://gitlab.com/group/repo1/-/merge_requests/7",
	}, status)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/repo1", "git", "remote", "get-url", "upstream"},
		{"work/group/repo1", "git", "remote", "get-url", "origin"},
		{"work/group/repo1", "glab", "repo", "view", originURL, "-F", "json", "--jq", ".id"},
		{"work/group/repo1", "glab", "api", "projects/group%2Frepo1/merge_requests?source_branch=turbolift-campaign&source_project_id=99&state=all"},
		{"work/group/repo1", "glab", "mr", "view", "7", "--repo", originURL, "-F", "json"},
		{"work/group/repo1", "glab", "api", "projects/99/merge_requests/7/approvals"},
	})
}

func TestGitLabReturnsNoPRFoundWhenTheBranchHasNoMergeRequest(t *testing.T) {
	execInstance = newCannedExecutor(map[string]canned{
		"git remote get-url upstream":                       noUpstream,
		"git remote get-url origin":                         {output: originURL + "\n"},
		"glab repo view " + originURL + " -F json --jq .id": originProjectID,
		"glab api projects/group%2Frepo1/merge_requests?source_branch=turbolift-campaign&source_project_id=99&state=all": {output: "[]"},
	})

	_, err := NewRealGitLab().GetPR(&strings.Builder{}, "work/group/repo1", "turbolift-campaign")
	assert.IsType(t, &NoPRFoundError{}, err)
}

func TestGitLabIgnoresMergeRequestsFromOtherForksWithTheSameBranch(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"git remote get-url upstream":                     {output: originURL + "\n"},
		"git remote get-url origin":                       {output: forkURL + "\n"},
		"glab repo view " + forkURL + " -F json --jq .id": {output: "42\n"},
		"glab api projects/group%2Frepo1/merge_requests?source_branch=turbolift-campaign&source_project_id=42&state=all": {output: `[{"iid":8,"project_id":99,"source_project_id":555},{"iid":7,"project_id":99,"source_project_id":42}]`},
		"glab mr view 7 --repo " + originURL + " -F json":                                                                {output: mrViewJSON},
		"glab api projects/99/merge_requests/7/approvals":                                                                {output: `{"approved":false}`},
	})
	execInstance = fakeExecutor

	status, err := NewRealGitLab().GetPR(&strings.Builder{}, "work/group/repo1", "turbolift-campaign")
	assert.NoError(t, err)
	assert.Equal(t, 7, status.Number)
}

func TestGitLabReturnsNoPRFoundWhenOnlyOtherForksHaveTheBranch(t *testing.T) {
	execInstance = newCannedExecutor(map[string]canned{
		"git remote get-url upstream":                     {output: originURL + "\n"},
		"git remote get-url origin":                       {output: forkURL + "\n"},
		"glab repo view " + forkURL + " -F json --jq .id": {output: "42\n"},
		"glab api projects/group%2Frepo1/merge_requests?source_branch=turbolift-campaign&source_project_id=42&state=opened": {output: `[{"iid":8,"project_id":99,"source_project_id":555}]`},
	})

	err := NewRealGitLab().ClosePullRequest(&strings.Builder{}, "work/group/repo1", "turbolift-campaign")
	assert.IsType(t, &NoPRFoundError{}, err)
}

func TestGitLabLooksUpTheMergeRequestInAListedRepoThatIsItselfAFork(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"git remote get-url upstream":                     {output: originURL + "\n"},
		"git remote get-url origin":                       {output: forkURL + "\n"},
		"glab repo view " + forkURL + " -F json --jq .id": {output: "42\n"},
		"glab api projects/me%2Frepo1/merge_requests?source_branch=turbolift-campaign&source_project_id=42&state=opened": {output: `[{"iid":3,"project_id":42,"source_project_id":42}]`},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().ClosePullRequest(&strings.Builder{}, "work/me/repo1", "turbolift-campaign")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/me/repo1", "git", "remote", "get-url", "upstream"},
		{"work/me/repo1", "git", "remote", "get-url", "origin"},
		{"work/me/repo1", "glab", "repo", "view", forkURL, "-F", "json", "--jq", ".id"},
		{"work/me/repo1", "glab", "api", "projects/me%2Frepo1/merge_requests?source_branch=turbolift-campaign&source_project_id=42&state=opened"},
		{"work/me/repo1", "glab", "mr", "close", "3", "--repo", forkURL},
	})
}

func TestGitLabMapsAMergedMergeRequestWithChangesRequested(t *testing.T) {
	status := gitLabPrStatus(gitLabMergeRequest{
		IID:                 7,
		State:               "merged",
		HasConflicts:        true,
		DetailedMergeStatus: "requested_changes",
		Downvotes:           1,
		HeadPipeline:        &gitLabPipeline{Status: "failed"},
	}, gitLabApprovals{Approved: false})

	assert.True(t, status.Closed)
	assert.Equal(t, "MERGED", status.State)
	assert.Equal(t, "CONFLICTING", status.Mergeable)
	assert.Equal(t, "CHANGES_REQUESTED", status.ReviewDecision)
	assert.Equal(t, []StatusCheckRollup{{State: "FAILURE"}}, status.StatusCheckRollup)
	assert.Equal(t, []ReactionGroup{{Content: "THUMBS_DOWN", Users: ReactionGroupUsers{TotalCount: 1}}}, status.ReactionGroups)
}

func TestGitLabMapsAnOpenMergeRequestThatNeedsApproval(t *testing.T) {
	status := gitLabPrStatus(gitLabMergeRequest{
		State:               "opened",
		DetailedMergeStatus: "not_approved",
		HeadPipeline:        &gitLabPipeline{Status: "running"},
	}, gitLabApprovals{Approved: false})

	assert.False(t, status.Closed)
	assert.Equal(t, "OPEN", status.State)
	assert.Equal(t, "UNKNOWN", status.Mergeable)
	assert.Equal(t, "REVIEW_REQUIRED", status.ReviewDecision)
	assert.Equal(t, []StatusCheckRollup{{State: "PENDING"}}, status.StatusCheckRollup)
	assert.Empty(t, status.ReactionGroups)
}

func TestGitLabMapsAClosedMergeRequestWithoutApprovalRulesOrPipeline(t *testing.T) {
	status := gitLabPrStatus(gitLabMergeRequest{State: "closed"}, gitLabApprovals{Approved: true})

	assert.True(t, status.Closed)
	assert.Equal(t, "CLOSED", status.State)
	assert.Equal(t, "", status.ReviewDecision)
	assert.Empty(t, status.StatusCheckRollup)
}

func TestGitLabClosesTheOpenMergeRequestOfTheBranch(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"git remote get-url upstream":                       noUpstream,
		"git remote get-url origin":                         {output: originURL + "\n"},
		"glab repo view " + originURL + " -F json --jq .id": originProjectID,
		"glab api projects/group%2Frepo1/merge_requests?source_branch=turbolift-campaign&source_project_id=99&state=opened": {output: mrListJSON},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().ClosePullRequest(&strings.Builder{}, "work/group/repo1", "turbolift-campaign")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/repo1", "git", "remote", "get-url", "upstream"},
		{"work/group/repo1", "git", "remote", "get-url", "origin"},
		{"work/group/repo1", "glab", "repo", "view", originURL, "-F", "json", "--jq", ".id"},
		{"work/group/repo1", "glab", "api", "projects/group%2Frepo1/merge_requests?source_branch=turbolift-campaign&source_project_id=99&state=opened"},
		{"work/group/repo1", "glab", "mr", "close", "7", "--repo", originURL},
	})
}

func TestGitLabReturnsNoPRFoundWhenClosingABranchWithoutAnOpenMergeRequest(t *testing.T) {
	execInstance = newCannedExecutor(map[string]canned{
		"git remote get-url upstream":                       noUpstream,
		"git remote get-url origin":                         {output: originURL + "\n"},
		"glab repo view " + originURL + " -F json --jq .id": originProjectID,
		"glab api projects/group%2Frepo1/merge_requests?source_branch=turbolift-campaign&source_project_id=99&state=opened": {output: "[]"},
	})

	err := NewRealGitLab().ClosePullRequest(&strings.Builder{}, "work/group/repo1", "turbolift-campaign")
	assert.IsType(t, &NoPRFoundError{}, err)
}

func TestGitLabUpdatesTheMergeRequestOfTheCurrentBranch(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"git rev-parse --abbrev-ref HEAD":                   {output: "turbolift-campaign\n"},
		"git remote get-url upstream":                       noUpstream,
		"git remote get-url origin":                         {output: originURL + "\n"},
		"glab repo view " + originURL + " -F json --jq .id": originProjectID,
		"glab api projects/group%2Frepo1/merge_requests?source_branch=turbolift-campaign&source_project_id=99&state=opened": {output: mrListJSON},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().UpdatePRDescription(&strings.Builder{}, "work/group/repo1", "new title", "new body")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/repo1", "git", "rev-parse", "--abbrev-ref", "HEAD"},
		{"work/group/repo1", "git", "remote", "get-url", "upstream"},
		{"work/group/repo1", "git", "remote", "get-url", "origin"},
		{"work/group/repo1", "glab", "repo", "view", originURL, "-F", "json", "--jq", ".id"},
		{"work/group/repo1", "glab", "api", "projects/group%2Frepo1/merge_requests?source_branch=turbolift-campaign&source_project_id=99&state=opened"},
		{"work/group/repo1", "glab", "mr", "update", "7", "--title", "new title", "--description", "new body", "--yes", "--repo", originURL},
	})
}

func TestGitLabReadsTheProtocolOfTheHostThatGitLabHostSelects(t *testing.T) {
	t.Setenv("GITLAB_HOST", "https://gitlab.example.com/")
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api projects/group%2Frepo1":                        {output: project(99, "group/repo1", "finished")},
		"glab config get git_protocol --host gitlab.example.com": {output: "https\n"},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().Clone(&strings.Builder{}, "work/group", "group/repo1")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group", "glab", "api", "projects/group%2Frepo1"},
		{"work/group", "glab", "config", "get", "git_protocol", "--host", "gitlab.example.com"},
		{"work/group", "git", "clone", "https://gitlab.com/group/repo1.git", "repo1"},
	})
}

func TestGitLabEscapesTheBranchNameInTheMergeRequestQuery(t *testing.T) {
	execInstance = newCannedExecutor(map[string]canned{
		"git remote get-url upstream":                       noUpstream,
		"git remote get-url origin":                         {output: originURL + "\n"},
		"glab repo view " + originURL + " -F json --jq .id": originProjectID,
		"glab api projects/group%2Frepo1/merge_requests?source_branch=feature%2Fx%2By&source_project_id=99&state=opened": {output: mrListJSON},
	})

	err := NewRealGitLab().ClosePullRequest(&strings.Builder{}, "work/group/repo1", "feature/x+y")
	assert.NoError(t, err)
}
