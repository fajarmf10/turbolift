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
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/skyscanner/turbolift/internal/platform"
)

const (
	gitLabDeveloperAccess = 30
	forkImportAttempts    = 60
	mergeRequestPageSize  = 100
)

var forkImportPollInterval = 2 * time.Second

type RealGitLab struct {
	users     map[string]gitLabUser
	protocols map[string]string
}

type gitLabUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
}

type gitLabMember struct {
	AccessLevel int `json:"access_level"`
}

type gitLabProject struct {
	ID                int    `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	SSHURLToRepo      string `json:"ssh_url_to_repo"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	ImportStatus      string `json:"import_status"`
}

type gitLabPipeline struct {
	Status string `json:"status"`
}

type gitLabMergeRequest struct {
	IID                 int             `json:"iid"`
	ProjectID           int             `json:"project_id"`
	SourceProjectID     int             `json:"source_project_id"`
	Title               string          `json:"title"`
	State               string          `json:"state"`
	SourceBranch        string          `json:"source_branch"`
	WebURL              string          `json:"web_url"`
	HasConflicts        bool            `json:"has_conflicts"`
	DetailedMergeStatus string          `json:"detailed_merge_status"`
	Upvotes             int             `json:"upvotes"`
	Downvotes           int             `json:"downvotes"`
	HeadPipeline        *gitLabPipeline `json:"head_pipeline"`
}

type gitLabApprovals struct {
	Approved   bool              `json:"approved"`
	ApprovedBy []json.RawMessage `json:"approved_by"`
}

type gitLabRemotes struct {
	targetName string
	targetURL  string
	originURL  string
	origin     *gitLabProject
}

// ForkAndClone works through the API and plain git instead of glab repo fork and glab repo clone. glab reuses
// any project with the same name as an existing fork, and a clone by URL neither selects the host nor adds the
// upstream remote.
func (r *RealGitLab) ForkAndClone(output io.Writer, workingDir string, fullRepoName string) error {
	host, namespace, repoName, err := platform.SplitGitLabPath(fullRepoName)
	if err != nil {
		return err
	}
	user, err := r.currentUser(output, workingDir, host)
	if err != nil {
		return err
	}
	parent, err := r.project(output, workingDir, host, url.PathEscape(namespace+"/"+repoName))
	if err != nil {
		return err
	}

	fork, err := r.ownedFork(output, workingDir, host, parent, user)
	if err != nil {
		return err
	}
	if fork == nil {
		fork, err = r.createFork(output, workingDir, host, parent)
		if err != nil {
			return err
		}
	}

	forkURL, err := r.cloneURL(output, workingDir, host, *fork)
	if err != nil {
		return err
	}
	parentURL, err := r.cloneURL(output, workingDir, host, parent)
	if err != nil {
		return err
	}

	if err := execInstance.Execute(output, workingDir, "git", "clone", forkURL, repoName); err != nil {
		return err
	}
	return execInstance.Execute(output, path.Join(workingDir, repoName), "git", "remote", "add", "upstream", parentURL)
}

func (r *RealGitLab) Clone(output io.Writer, workingDir string, fullRepoName string) error {
	host, namespace, repoName, err := platform.SplitGitLabPath(fullRepoName)
	if err != nil {
		return err
	}
	project, err := r.project(output, workingDir, host, url.PathEscape(namespace+"/"+repoName))
	if err != nil {
		return err
	}
	cloneURL, err := r.cloneURL(output, workingDir, host, project)
	if err != nil {
		return err
	}

	return execInstance.Execute(output, workingDir, "git", "clone", cloneURL, repoName)
}

func (r *RealGitLab) CreatePullRequest(output io.Writer, workingDir string, pr PullRequest) (didCreate bool, err error) {
	defaultBranch, err := r.GetDefaultBranchName(output, workingDir, pr.UpstreamRepo)
	if err != nil {
		return false, err
	}

	remotes, err := r.remotes(output, workingDir)
	if err != nil {
		return false, err
	}

	// GitLab accepts a merge request with no changes, so we check for new commits first, like GitHub does.
	if err := execInstance.Execute(output, workingDir, "git", "fetch", remotes.targetName, defaultBranch); err != nil {
		return false, err
	}
	commitsAhead, err := execInstance.ExecuteAndCapture(output, workingDir, "git", "rev-list", "--count", "FETCH_HEAD..HEAD")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(commitsAhead) == "0" {
		return false, nil
	}

	glabArgs := []string{
		"mr",
		"create",
		"--title",
		pr.Title,
		"--description",
		pr.Body,
		"--repo",
		glabRepo(pr.UpstreamRepo),
		"--head",
		remotes.originURL,
		"--target-branch",
		defaultBranch,
		"--yes",
	}

	if pr.IsDraft {
		glabArgs = append(glabArgs, "--draft")
	}

	if err := execInstance.Execute(output, workingDir, "glab", glabArgs...); err != nil {
		return false, err
	}
	return true, nil
}

func (r *RealGitLab) ClosePullRequest(output io.Writer, workingDir string, branchName string) error {
	mr, repoURL, err := r.findMergeRequest(output, workingDir, branchName, false)
	if err != nil {
		return err
	}

	return execInstance.Execute(output, workingDir, "glab", "mr", "close", strconv.Itoa(mr.IID), "--repo", repoURL)
}

func (r *RealGitLab) UpdatePRDescription(output io.Writer, workingDir string, title string, body string) error {
	branchName, err := execInstance.ExecuteAndCapture(output, workingDir, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}

	mr, repoURL, err := r.findMergeRequest(output, workingDir, strings.TrimSpace(branchName), false)
	if err != nil {
		return err
	}

	return execInstance.Execute(output, workingDir, "glab", "mr", "update", strconv.Itoa(mr.IID), "--title", title, "--description", body, "--yes", "--repo", repoURL)
}

func (r *RealGitLab) GetPR(output io.Writer, workingDir string, branchName string) (*PrStatus, error) {
	found, repoURL, err := r.findMergeRequest(output, workingDir, branchName, true)
	if err != nil {
		return nil, err
	}

	s, err := execInstance.ExecuteAndCapture(output, workingDir, "glab", "mr", "view", strconv.Itoa(found.IID), "--repo", repoURL, "-F", "json")
	if err != nil {
		return nil, err
	}
	var mr gitLabMergeRequest
	if err := json.Unmarshal([]byte(s), &mr); err != nil {
		return nil, fmt.Errorf("unable to unmarshall the merge request: %w", err)
	}

	// Approvals are optional, because a GitLab without them still has the merge request, and the review state then
	// comes from the merge status alone.
	var approvals *gitLabApprovals
	s, err = r.api(output, workingDir, remoteHostname(repoURL), fmt.Sprintf("projects/%d/merge_requests/%d/approvals", mr.ProjectID, mr.IID))
	if err == nil {
		var read gitLabApprovals
		if json.Unmarshal([]byte(s), &read) == nil {
			approvals = &read
		}
	}

	return gitLabPrStatus(mr, approvals), nil
}

func (r *RealGitLab) GetDefaultBranchName(output io.Writer, workingDir string, fullRepoName string) (string, error) {
	host, namespace, repoName, err := platform.SplitGitLabPath(fullRepoName)
	if err != nil {
		return "", err
	}
	project, err := r.project(output, workingDir, host, url.PathEscape(namespace+"/"+repoName))
	return project.DefaultBranch, err
}

// IsPushable reads the membership from members/all, because the permissions field of a project stays empty
// when the access comes from a parent group.
func (r *RealGitLab) IsPushable(output io.Writer, repo string) (bool, error) {
	currentDir, err := os.Getwd()
	if err != nil {
		return false, err
	}
	host, namespace, repoName, err := platform.SplitGitLabPath(repo)
	if err != nil {
		return false, err
	}
	user, err := r.currentUser(output, currentDir, host)
	if err != nil {
		return false, err
	}

	s, err := r.api(output, currentDir, host, fmt.Sprintf("projects/%s/members/all/%d", url.PathEscape(namespace+"/"+repoName), user.ID))
	if err != nil {
		if strings.Contains(s, "HTTP 404") {
			return false, nil
		}
		return false, err
	}

	var member gitLabMember
	if err := json.Unmarshal([]byte(s), &member); err != nil {
		return false, err
	}
	return member.AccessLevel >= gitLabDeveloperAccess, nil
}

func (r *RealGitLab) currentUser(output io.Writer, workingDir string, host string) (gitLabUser, error) {
	if user, ok := r.users[host]; ok {
		return user, nil
	}

	s, err := r.api(output, workingDir, host, "user")
	if err != nil {
		return gitLabUser{}, err
	}
	var user gitLabUser
	if err := json.Unmarshal([]byte(s), &user); err != nil {
		return gitLabUser{}, fmt.Errorf("unable to unmarshall the current GitLab user: %w", err)
	}

	r.users[host] = user
	return user, nil
}

func (r *RealGitLab) project(output io.Writer, workingDir string, host string, idOrEscapedPath string) (gitLabProject, error) {
	s, err := r.api(output, workingDir, host, "projects/"+idOrEscapedPath)
	if err != nil {
		return gitLabProject{}, err
	}
	var project gitLabProject
	if err := json.Unmarshal([]byte(s), &project); err != nil {
		return gitLabProject{}, fmt.Errorf("unable to unmarshall the GitLab project: %w", err)
	}
	return project, nil
}

// ownedFork returns the fork of parent in the user's own namespace, found by its parent rather than by name,
// so a project with the same name that forks something else is never used.
func (r *RealGitLab) ownedFork(output io.Writer, workingDir string, host string, parent gitLabProject, user gitLabUser) (*gitLabProject, error) {
	s, err := r.api(output, workingDir, host, fmt.Sprintf("projects/%d/forks?owned=true&per_page=100", parent.ID), "--paginate", "--output", "ndjson")
	if err != nil {
		return nil, err
	}
	forks, err := decodeJSONLines[gitLabProject](s)
	if err != nil {
		return nil, fmt.Errorf("unable to unmarshall the forks of %s: %w", parent.PathWithNamespace, err)
	}
	for _, fork := range forks {
		if strings.HasPrefix(fork.PathWithNamespace, user.Username+"/") {
			return &fork, nil
		}
	}
	return nil, nil
}

func (r *RealGitLab) createFork(output io.Writer, workingDir string, host string, parent gitLabProject) (*gitLabProject, error) {
	s, err := r.api(output, workingDir, host, fmt.Sprintf("projects/%d/fork", parent.ID), "-X", "POST")
	if err != nil {
		return nil, fmt.Errorf("unable to fork %s into your namespace: %w", parent.PathWithNamespace, err)
	}
	var fork gitLabProject
	if err := json.Unmarshal([]byte(s), &fork); err != nil {
		return nil, fmt.Errorf("unable to unmarshall the fork of %s: %w", parent.PathWithNamespace, err)
	}

	// GitLab copies the repository into a new fork in the background, so we wait before we clone it.
	for attempt := 1; ; attempt++ {
		switch fork.ImportStatus {
		case "", "none", "finished":
			return &fork, nil
		case "failed":
			return nil, fmt.Errorf("GitLab failed to create the fork %s", fork.PathWithNamespace)
		}
		if attempt == forkImportAttempts {
			return nil, fmt.Errorf("the fork %s is not ready yet, run the clone again later", fork.PathWithNamespace)
		}
		time.Sleep(forkImportPollInterval)
		fork, err = r.project(output, workingDir, host, strconv.Itoa(fork.ID))
		if err != nil {
			return nil, err
		}
	}
}

// cloneURL reads git_protocol for an explicit host, because glab auth login stores it per host and the global
// value can differ.
func (r *RealGitLab) cloneURL(output io.Writer, workingDir string, host string, project gitLabProject) (string, error) {
	protocol, ok := r.protocols[host]
	if !ok {
		protocolHost := host
		if protocolHost == "" {
			var err error
			protocolHost, err = r.defaultHost(output, workingDir)
			if err != nil {
				return "", err
			}
		}
		s, err := execInstance.ExecuteAndCapture(output, workingDir, "glab", "config", "get", "git_protocol", "--host", protocolHost)
		if err != nil {
			return "", err
		}
		protocol = strings.TrimSpace(s)
		r.protocols[host] = protocol
	}

	if protocol == "https" || protocol == "http" {
		return project.HTTPURLToRepo, nil
	}
	return project.SSHURLToRepo, nil
}

func (r *RealGitLab) defaultHost(output io.Writer, workingDir string) (string, error) {
	if host := os.Getenv("GITLAB_HOST"); host != "" {
		host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		return strings.TrimSuffix(host, "/"), nil
	}
	s, err := execInstance.ExecuteAndCapture(output, workingDir, "glab", "config", "get", "host")
	if err != nil {
		return "", err
	}
	if host := strings.TrimSpace(s); host != "" {
		return host, nil
	}
	return "gitlab.com", nil
}

// findMergeRequest passes remote URLs to glab with --repo instead of calling the API itself, because glab then maps
// each URL to its host, port, subfolder and credentials the same way as for every other merge request command.
func (r *RealGitLab) findMergeRequest(output io.Writer, workingDir string, branchName string, anyState bool) (*gitLabMergeRequest, string, error) {
	remotes, err := r.remotes(output, workingDir)
	if err != nil {
		return nil, "", err
	}
	origin := remotes.origin
	if origin == nil {
		project, err := r.remoteProject(output, workingDir, remotes.originURL)
		if err != nil {
			return nil, "", err
		}
		origin = &project
	}

	for page := 1; ; page++ {
		glabArgs := []string{"mr", "list", "--repo", remotes.targetURL, "--source-branch", branchName, "-F", "json", "--per-page", strconv.Itoa(mergeRequestPageSize), "--page", strconv.Itoa(page)}
		if anyState {
			glabArgs = append(glabArgs, "--all")
		}
		s, err := execInstance.ExecuteAndCapture(output, workingDir, "glab", glabArgs...)
		if err != nil {
			return nil, "", err
		}
		var mrs []gitLabMergeRequest
		if err := json.Unmarshal([]byte(s), &mrs); err != nil {
			return nil, "", fmt.Errorf("unable to unmarshall the merge request list: %w", err)
		}

		// Another fork can open a merge request from a branch with the same name, so the source project must match.
		for _, mr := range mrs {
			if mr.SourceProjectID == origin.ID {
				return &mr, remotes.targetURL, nil
			}
		}
		if len(mrs) < mergeRequestPageSize {
			return nil, "", &NoPRFoundError{Path: workingDir, BranchName: branchName}
		}
	}
}

func (r *RealGitLab) remoteProject(output io.Writer, workingDir string, remoteURL string) (gitLabProject, error) {
	s, err := execInstance.ExecuteAndCapture(output, workingDir, "glab", "repo", "view", remoteURL, "-F", "json")
	if err != nil {
		return gitLabProject{}, err
	}
	var project gitLabProject
	if err := json.Unmarshal([]byte(s), &project); err != nil {
		return gitLabProject{}, fmt.Errorf("unable to unmarshall the GitLab project of %s: %w", remoteURL, err)
	}
	return project, nil
}

// remotes finds the remote that merge requests go to. That is upstream in a clone that turbolift forked, and
// origin otherwise. A clone made by other tools can have an upstream remote that points at the parent of the
// listed project, so origin wins when GitLab names it as the project of the campaign directory.
func (r *RealGitLab) remotes(output io.Writer, workingDir string) (gitLabRemotes, error) {
	upstreamURL, upstreamErr := r.remoteURL(output, workingDir, "upstream")
	originURL, err := r.remoteURL(output, workingDir, "origin")
	if err != nil {
		return gitLabRemotes{}, err
	}
	if upstreamErr != nil {
		return gitLabRemotes{targetName: "origin", targetURL: originURL, originURL: originURL}, nil
	}

	origin, err := r.remoteProject(output, workingDir, originURL)
	if err != nil {
		return gitLabRemotes{}, err
	}
	if strings.EqualFold(origin.PathWithNamespace, campaignProjectPath(workingDir)) {
		return gitLabRemotes{targetName: "origin", targetURL: originURL, originURL: originURL, origin: &origin}, nil
	}
	return gitLabRemotes{targetName: "upstream", targetURL: upstreamURL, originURL: originURL, origin: &origin}, nil
}

func (r *RealGitLab) remoteURL(output io.Writer, workingDir string, remoteName string) (string, error) {
	remoteURL, err := execInstance.ExecuteAndCapture(output, workingDir, "git", "remote", "get-url", remoteName)
	return strings.TrimSpace(remoteURL), err
}

func (r *RealGitLab) api(output io.Writer, workingDir string, host string, endpoint string, extraArgs ...string) (string, error) {
	glabArgs := append([]string{"api", endpoint}, extraArgs...)
	if host != "" {
		glabArgs = append(glabArgs, "--hostname", host)
	}
	return execInstance.ExecuteAndCapture(output, workingDir, "glab", glabArgs...)
}

// glabRepo turns host/group/repo into a URL, because glab reads the host of host/group/repo as a group name.
func glabRepo(fullRepoName string) string {
	host, namespace, repoName, err := platform.SplitGitLabPath(fullRepoName)
	if err != nil || host == "" {
		return fullRepoName
	}
	return "https://" + path.Join(host, namespace, repoName)
}

// remoteHostname returns the host of a remote URL the way glab names it in its settings: without a port, in
// lower case and without www. glab --hostname cannot take a port, so for a remote with an explicit HTTPS port the
// approvals call can miss the host settings, and pr-status then leaves the Reviews column empty.
func remoteHostname(remoteURL string) string {
	var host string
	if parsed, err := url.Parse(remoteURL); err == nil && parsed.Host != "" {
		host = parsed.Hostname()
	} else {
		host, _, _ = strings.Cut(remoteURL, ":")
		if _, afterUser, found := strings.Cut(host, "@"); found {
			host = afterUser
		}
	}
	return strings.TrimPrefix(strings.ToLower(host), "www.")
}

func campaignProjectPath(workingDir string) string {
	projectPath, found := strings.CutPrefix(path.Clean(workingDir), "work/")
	if !found {
		return ""
	}
	return projectPath
}

func decodeJSONLines[T any](s string) ([]T, error) {
	decoder := json.NewDecoder(strings.NewReader(s))
	var values []T
	for {
		var value T
		if err := decoder.Decode(&value); err == io.EOF {
			return values, nil
		} else if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
}

func gitLabPrStatus(mr gitLabMergeRequest, approvals *gitLabApprovals) *PrStatus {
	status := &PrStatus{
		Closed:         mr.State == "closed" || mr.State == "merged",
		HeadRefName:    mr.SourceBranch,
		Mergeable:      "UNKNOWN",
		Number:         mr.IID,
		ReviewDecision: "",
		State:          strings.ToUpper(mr.State),
		Title:          mr.Title,
		Url:            mr.WebURL,
	}

	if mr.State == "opened" {
		status.State = "OPEN"
	}

	if mr.HasConflicts {
		status.Mergeable = "CONFLICTING"
	} else if mr.DetailedMergeStatus == "mergeable" {
		status.Mergeable = "MERGEABLE"
	}

	switch {
	case mr.DetailedMergeStatus == "requested_changes":
		status.ReviewDecision = "CHANGES_REQUESTED"
	case approvals == nil:
		if mr.DetailedMergeStatus == "not_approved" {
			status.ReviewDecision = "REVIEW_REQUIRED"
		}
	case !approvals.Approved:
		status.ReviewDecision = "REVIEW_REQUIRED"
	case len(approvals.ApprovedBy) > 0:
		status.ReviewDecision = "APPROVED"
	}

	if mr.HeadPipeline != nil {
		status.StatusCheckRollup = []StatusCheckRollup{{State: gitLabCheckState(mr.HeadPipeline.Status)}}
	}

	if mr.Upvotes > 0 {
		status.ReactionGroups = append(status.ReactionGroups, ReactionGroup{Content: "THUMBS_UP", Users: ReactionGroupUsers{TotalCount: mr.Upvotes}})
	}
	if mr.Downvotes > 0 {
		status.ReactionGroups = append(status.ReactionGroups, ReactionGroup{Content: "THUMBS_DOWN", Users: ReactionGroupUsers{TotalCount: mr.Downvotes}})
	}

	return status
}

func gitLabCheckState(pipelineStatus string) string {
	switch pipelineStatus {
	case "success", "skipped":
		return "SUCCESS"
	case "failed", "canceled":
		return "FAILURE"
	default:
		return "PENDING"
	}
}

func NewRealGitLab() *RealGitLab {
	return &RealGitLab{users: map[string]gitLabUser{}, protocols: map[string]string{}}
}
