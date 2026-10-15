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
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/skyscanner/turbolift/internal/executor"
	"github.com/skyscanner/turbolift/internal/platform"
)

const (
	originURL   = "git@gitlab.com:group/repo1.git"
	forkURL     = "git@gitlab.com:me/repo1.git"
	mrListJSON  = `[{"iid":7,"project_id":99,"state":"opened","source_branch":"turbolift-campaign"}]`
	currentUser = `{"id":42,"username":"me"}`
)

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

func TestGitLabClonesIntoTheRepoNameDirectory(t *testing.T) {
	fakeExecutor := executor.NewAlwaysSucceedsFakeExecutor()
	execInstance = fakeExecutor

	err := NewRealGitLab().Clone(&strings.Builder{}, "work/group/subgroup", "group/subgroup/repo1")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/subgroup", "glab", "repo", "clone", "group/subgroup/repo1", "repo1"},
	})
}

func TestGitLabClonesAHostPrefixedRepoByURL(t *testing.T) {
	fakeExecutor := executor.NewAlwaysSucceedsFakeExecutor()
	execInstance = fakeExecutor

	err := NewRealGitLab().Clone(&strings.Builder{}, "work/group/subgroup", "gitlab.com/group/subgroup/repo1")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/subgroup", "glab", "repo", "clone", "https://gitlab.com/group/subgroup/repo1", "repo1"},
	})
}

func TestGitLabReadsTheDefaultBranchOfAHostPrefixedRepoByURL(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab repo view https://gitlab.com/group/repo1 -F json --jq .default_branch": {output: "main\n"},
	})
	execInstance = fakeExecutor

	branch, err := NewRealGitLab().GetDefaultBranchName(&strings.Builder{}, "work/group/repo1", "gitlab.com/group/repo1")
	assert.NoError(t, err)
	assert.Equal(t, "main", branch)
}

func TestGitLabReturnsErrorOnFailedClone(t *testing.T) {
	execInstance = executor.NewAlwaysFailsFakeExecutor()

	err := NewRealGitLab().Clone(&strings.Builder{}, "work/group", "group/repo1")
	assert.Error(t, err)
}

func TestGitLabForksAndThenClonesTheForkFromTheUserNamespace(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api user": {output: currentUser},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().ForkAndClone(&strings.Builder{}, "work/group/subgroup", "group/subgroup/repo1")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/subgroup", "glab", "api", "user"},
		{"work/group/subgroup", "glab", "repo", "fork", "group/subgroup/repo1", "--clone=false"},
		{"work/group/subgroup", "glab", "repo", "clone", "me/repo1", "repo1"},
	})
}

func TestGitLabForksOnASelfManagedHost(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api user --hostname gitlab.example.com": {output: currentUser},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().ForkAndClone(&strings.Builder{}, "work/group", "gitlab.example.com/group/repo1")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group", "glab", "api", "user", "--hostname", "gitlab.example.com"},
		{"work/group", "glab", "repo", "fork", "https://gitlab.example.com/group/repo1", "--clone=false"},
		{"work/group", "glab", "repo", "clone", "https://gitlab.example.com/me/repo1", "repo1"},
	})
}

func TestGitLabDoesNotCloneWhenTheForkFails(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab api user": {output: currentUser},
		"glab repo fork group/repo1 --clone=false": {err: errors.New("synthetic error")},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().ForkAndClone(&strings.Builder{}, "work/group", "group/repo1")
	assert.Error(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group", "glab", "api", "user"},
		{"work/group", "glab", "repo", "fork", "group/repo1", "--clone=false"},
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
		"glab repo view group/repo1 -F json --jq .default_branch": {output: "main\n"},
	})
	execInstance = fakeExecutor

	branch, err := NewRealGitLab().GetDefaultBranchName(&strings.Builder{}, "work/group/repo1", "group/repo1")
	assert.NoError(t, err)
	assert.Equal(t, "main", branch)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/repo1", "glab", "repo", "view", "group/repo1", "-F", "json", "--jq", ".default_branch"},
	})
}

func createMergeRequestResponses(commitsAhead string) map[string]canned {
	return map[string]canned{
		"glab repo view group/repo1 -F json --jq .default_branch": {output: "main\n"},
		"git remote get-url upstream":                             noUpstream,
		"git remote get-url origin":                               {output: originURL + "\n"},
		"git rev-list --count FETCH_HEAD..HEAD":                   {output: commitsAhead + "\n"},
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
		{"work/group/repo1", "glab", "repo", "view", "group/repo1", "-F", "json", "--jq", ".default_branch"},
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
		{"work/group/repo1", "glab", "repo", "view", "group/repo1", "-F", "json", "--jq", ".default_branch"},
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
		{"work/group/repo1", "glab", "repo", "view", "group/repo1", "-F", "json", "--jq", ".default_branch"},
		{"work/group/repo1", "git", "remote", "get-url", "upstream"},
		{"work/group/repo1", "git", "remote", "get-url", "origin"},
		{"work/group/repo1", "git", "fetch", "origin", "main"},
		{"work/group/repo1", "git", "rev-list", "--count", "FETCH_HEAD..HEAD"},
		{"work/group/repo1", "glab", "mr", "create", "--title", "some title", "--description", "some body", "--repo", "group/repo1", "--head", originURL, "--target-branch", "main", "--yes", "--draft"},
	})
}

func TestGitLabCreatesAMergeRequestFromAFork(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"glab repo view group/repo1 -F json --jq .default_branch": {output: "main\n"},
		"git remote get-url upstream":                             {output: originURL + "\n"},
		"git remote get-url origin":                               {output: forkURL + "\n"},
		"git rev-list --count FETCH_HEAD..HEAD":                   {output: "1\n"},
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
		{"work/group/repo1", "glab", "repo", "view", "group/repo1", "-F", "json", "--jq", ".default_branch"},
		{"work/group/repo1", "git", "remote", "get-url", "upstream"},
		{"work/group/repo1", "git", "fetch", "upstream", "main"},
		{"work/group/repo1", "git", "rev-list", "--count", "FETCH_HEAD..HEAD"},
		{"work/group/repo1", "git", "remote", "get-url", "origin"},
		{"work/group/repo1", "glab", "mr", "create", "--title", "some title", "--description", "some body", "--repo", "group/repo1", "--head", forkURL, "--target-branch", "main", "--yes"},
	})
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
		"git remote get-url upstream": noUpstream,
		"git remote get-url origin":   {output: originURL + "\n"},
		"glab mr list --repo " + originURL + " --source-branch turbolift-campaign -F json --all": {output: mrListJSON},
		"glab mr view 7 --repo " + originURL + " -F json":                                        {output: `{"iid":7,"project_id":99,"title":"some title","state":"opened","source_branch":"turbolift-campaign","web_url":"https://gitlab.com/group/repo1/-/merge_requests/7","has_conflicts":false,"detailed_merge_status":"mergeable","upvotes":2,"downvotes":0,"head_pipeline":{"status":"success"}}`},
		"glab api projects/99/merge_requests/7/approvals":                                        {output: `{"approved":true,"approved_by":[{"user":{"username":"reviewer"}}]}`},
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
		{"work/group/repo1", "glab", "mr", "list", "--repo", originURL, "--source-branch", "turbolift-campaign", "-F", "json", "--all"},
		{"work/group/repo1", "glab", "mr", "view", "7", "--repo", originURL, "-F", "json"},
		{"work/group/repo1", "glab", "api", "projects/99/merge_requests/7/approvals"},
	})
}

func TestGitLabReturnsNoPRFoundWhenTheBranchHasNoMergeRequest(t *testing.T) {
	execInstance = newCannedExecutor(map[string]canned{
		"git remote get-url upstream": noUpstream,
		"git remote get-url origin":   {output: originURL + "\n"},
		"glab mr list --repo " + originURL + " --source-branch turbolift-campaign -F json --all": {output: "[]"},
	})

	_, err := NewRealGitLab().GetPR(&strings.Builder{}, "work/group/repo1", "turbolift-campaign")
	assert.IsType(t, &NoPRFoundError{}, err)
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
		"git remote get-url upstream": noUpstream,
		"git remote get-url origin":   {output: originURL + "\n"},
		"glab mr list --repo " + originURL + " --source-branch turbolift-campaign -F json": {output: mrListJSON},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().ClosePullRequest(&strings.Builder{}, "work/group/repo1", "turbolift-campaign")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/repo1", "git", "remote", "get-url", "upstream"},
		{"work/group/repo1", "git", "remote", "get-url", "origin"},
		{"work/group/repo1", "glab", "mr", "list", "--repo", originURL, "--source-branch", "turbolift-campaign", "-F", "json"},
		{"work/group/repo1", "glab", "mr", "close", "7", "--repo", originURL},
	})
}

func TestGitLabReturnsNoPRFoundWhenClosingABranchWithoutAnOpenMergeRequest(t *testing.T) {
	execInstance = newCannedExecutor(map[string]canned{
		"git remote get-url upstream": noUpstream,
		"git remote get-url origin":   {output: originURL + "\n"},
		"glab mr list --repo " + originURL + " --source-branch turbolift-campaign -F json": {output: "[]"},
	})

	err := NewRealGitLab().ClosePullRequest(&strings.Builder{}, "work/group/repo1", "turbolift-campaign")
	assert.IsType(t, &NoPRFoundError{}, err)
}

func TestGitLabUpdatesTheMergeRequestOfTheCurrentBranch(t *testing.T) {
	fakeExecutor := newCannedExecutor(map[string]canned{
		"git rev-parse --abbrev-ref HEAD": {output: "turbolift-campaign\n"},
		"git remote get-url upstream":     noUpstream,
		"git remote get-url origin":       {output: originURL + "\n"},
		"glab mr list --repo " + originURL + " --source-branch turbolift-campaign -F json": {output: mrListJSON},
	})
	execInstance = fakeExecutor

	err := NewRealGitLab().UpdatePRDescription(&strings.Builder{}, "work/group/repo1", "new title", "new body")
	assert.NoError(t, err)

	fakeExecutor.AssertCalledWith(t, [][]string{
		{"work/group/repo1", "git", "rev-parse", "--abbrev-ref", "HEAD"},
		{"work/group/repo1", "git", "remote", "get-url", "upstream"},
		{"work/group/repo1", "git", "remote", "get-url", "origin"},
		{"work/group/repo1", "glab", "mr", "list", "--repo", originURL, "--source-branch", "turbolift-campaign", "-F", "json"},
		{"work/group/repo1", "glab", "mr", "update", "7", "--title", "new title", "--description", "new body", "--yes", "--repo", originURL},
	})
}
