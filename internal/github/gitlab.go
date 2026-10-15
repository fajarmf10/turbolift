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

	"github.com/skyscanner/turbolift/internal/platform"
)

const gitLabDeveloperAccess = 30

type RealGitLab struct {
	users map[string]gitLabUser
}

type gitLabUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
}

type gitLabMember struct {
	AccessLevel int `json:"access_level"`
}

type gitLabPipeline struct {
	Status string `json:"status"`
}

type gitLabMergeRequest struct {
	IID                 int             `json:"iid"`
	ProjectID           int             `json:"project_id"`
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

func (r *RealGitLab) ForkAndClone(output io.Writer, workingDir string, fullRepoName string) error {
	host, _, repoName, err := platform.SplitGitLabPath(fullRepoName)
	if err != nil {
		return err
	}
	user, err := r.currentUser(output, workingDir, host)
	if err != nil {
		return err
	}

	// glab skips the clone when the fork already exists, so we fork and clone as two steps. Cloning a fork
	// we own also adds the upstream remote that the clone command pulls from.
	if err := execInstance.Execute(output, workingDir, "glab", "repo", "fork", fullRepoName, "--clone=false"); err != nil {
		return err
	}
	return execInstance.Execute(output, workingDir, "glab", "repo", "clone", path.Join(host, user.Username, repoName), repoName)
}

func (r *RealGitLab) Clone(output io.Writer, workingDir string, fullRepoName string) error {
	return execInstance.Execute(output, workingDir, "glab", "repo", "clone", fullRepoName, path.Base(fullRepoName))
}

func (r *RealGitLab) CreatePullRequest(output io.Writer, workingDir string, pr PullRequest) (didCreate bool, err error) {
	defaultBranch, err := r.GetDefaultBranchName(output, workingDir, pr.UpstreamRepo)
	if err != nil {
		return false, err
	}

	remoteName, remoteURL, err := r.targetRemote(output, workingDir)
	if err != nil {
		return false, err
	}

	// GitLab accepts a merge request with no changes, so we check for new commits first, like GitHub does.
	if err := execInstance.Execute(output, workingDir, "git", "fetch", remoteName, defaultBranch); err != nil {
		return false, err
	}
	commitsAhead, err := execInstance.ExecuteAndCapture(output, workingDir, "git", "rev-list", "--count", "FETCH_HEAD..HEAD")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(commitsAhead) == "0" {
		return false, nil
	}

	headURL := remoteURL
	if remoteName != "origin" {
		headURL, err = r.remoteURL(output, workingDir, "origin")
		if err != nil {
			return false, err
		}
	}

	glabArgs := []string{
		"mr",
		"create",
		"--title",
		pr.Title,
		"--description",
		pr.Body,
		"--repo",
		pr.UpstreamRepo,
		"--head",
		headURL,
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

	s, err = execInstance.ExecuteAndCapture(output, workingDir, "glab", "api", fmt.Sprintf("projects/%d/merge_requests/%d/approvals", mr.ProjectID, mr.IID))
	if err != nil {
		return nil, err
	}
	var approvals gitLabApprovals
	if err := json.Unmarshal([]byte(s), &approvals); err != nil {
		return nil, fmt.Errorf("unable to unmarshall the merge request approvals: %w", err)
	}

	return gitLabPrStatus(mr, approvals), nil
}

func (r *RealGitLab) GetDefaultBranchName(output io.Writer, workingDir string, fullRepoName string) (string, error) {
	defaultBranch, err := execInstance.ExecuteAndCapture(output, workingDir, "glab", "repo", "view", fullRepoName, "-F", "json", "--jq", ".default_branch")
	return strings.TrimSpace(defaultBranch), err
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

	endpoint := fmt.Sprintf("projects/%s/members/all/%d", url.PathEscape(namespace+"/"+repoName), user.ID)
	s, err := execInstance.ExecuteAndCapture(output, currentDir, "glab", glabAPIArgs(host, endpoint)...)
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

	s, err := execInstance.ExecuteAndCapture(output, workingDir, "glab", glabAPIArgs(host, "user")...)
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

func (r *RealGitLab) findMergeRequest(output io.Writer, workingDir string, branchName string, anyState bool) (*gitLabMergeRequest, string, error) {
	_, repoURL, err := r.targetRemote(output, workingDir)
	if err != nil {
		return nil, "", err
	}

	glabArgs := []string{"mr", "list", "--repo", repoURL, "--source-branch", branchName, "-F", "json"}
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
	if len(mrs) == 0 {
		return nil, "", &NoPRFoundError{Path: workingDir, BranchName: branchName}
	}
	return &mrs[0], repoURL, nil
}

// targetRemote returns the remote that merge requests go to: upstream in a fork, origin otherwise.
func (r *RealGitLab) targetRemote(output io.Writer, workingDir string) (string, string, error) {
	if remoteURL, err := r.remoteURL(output, workingDir, "upstream"); err == nil {
		return "upstream", remoteURL, nil
	}
	remoteURL, err := r.remoteURL(output, workingDir, "origin")
	return "origin", remoteURL, err
}

func (r *RealGitLab) remoteURL(output io.Writer, workingDir string, remoteName string) (string, error) {
	remoteURL, err := execInstance.ExecuteAndCapture(output, workingDir, "git", "remote", "get-url", remoteName)
	return strings.TrimSpace(remoteURL), err
}

func glabAPIArgs(host string, endpoint string) []string {
	if host == "" {
		return []string{"api", endpoint}
	}
	return []string{"api", endpoint, "--hostname", host}
}

func gitLabPrStatus(mr gitLabMergeRequest, approvals gitLabApprovals) *PrStatus {
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
	return &RealGitLab{users: map[string]gitLabUser{}}
}
